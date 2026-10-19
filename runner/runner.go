// Package runner runs applets in a WebAssembly sandbox.
//
// An applet is a WebAssembly module as produced by the build package, or
// compatible with it: it exports _start, and transforms a stream of JSON
// objects according to the protocol below. Besides WASI, it may only import
// from dependency modules given to NewEngine, such as the Javy plugin for
// applets built by package build. Each run gets fresh instances of the applet
// and its dependencies, with no filesystem, no network, a fixed clock and a
// deterministic random source.
//
// The protocol between host and applet uses the standard streams, one line
// per message:
//
//   - stdin: the first line is the params (JSON, null if there are none),
//     followed by one input object per line. EOF means the host is done
//     sending input.
//   - stdout: one output object per line.
//   - stderr: one record per line: {"log": "..."} to log a line,
//     {"done": true} when the applet is done, which must be its last output,
//     or {"error": "...", "stack": "...", "input": <index or null>} when it
//     fails. Lines that are not records are logged as they are.
//
// A run succeeds only if _start returns and the applet reported done.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Engine loads and runs applets. It is safe for concurrent use.
type Engine struct {
	rt   wazero.Runtime
	deps map[string]wazero.CompiledModule
}

// NewEngine compiles the dependency modules, which applets may import from
// by their name in deps. Dependencies may only import WASI. Compiling can
// take a while; create one Engine and reuse it.
func NewEngine(ctx context.Context, deps map[string][]byte, limits Limits) (*Engine, error) {
	limits = limits.withDefaults()
	pages := limits.memoryMiB * 16
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(!limits.noCloseOnContextDone).
		WithMemoryLimitPages(pages))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("cannot instantiate wasi: %w", err)
	}

	e := &Engine{rt: rt, deps: map[string]wazero.CompiledModule{}}
	for name, wasm := range deps {
		mod, err := rt.CompileModule(ctx, wasm)
		if err != nil {
			rt.Close(ctx)
			return nil, fmt.Errorf("cannot compile dependency %q: %w", name, err)
		}
		e.deps[name] = mod
	}

	return e, nil
}

// Close releases the engine. Runs in progress fail.
func (e *Engine) Close(ctx context.Context) error {
	return e.rt.Close(ctx)
}

// Applet is a loaded applet, which can be run many times.
type Applet struct {
	e   *Engine
	mod wazero.CompiledModule
}

// Load compiles an applet module.
func (e *Engine) Load(ctx context.Context, wasm []byte) (*Applet, error) {
	mod, err := e.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("cannot compile applet: %w", err)
	}
	for _, f := range mod.ImportedFunctions() {
		if name, _, _ := f.Import(); name != wasi_snapshot_preview1.ModuleName && e.deps[name] == nil {
			mod.Close(ctx)
			return nil, fmt.Errorf("applet imports from unknown module %q", name)
		}
	}
	if _, ok := mod.ExportedFunctions()["_start"]; !ok {
		mod.Close(ctx)
		return nil, errors.New("applet does not export _start")
	}
	return &Applet{e: e, mod: mod}, nil
}

// Close releases the applet.
func (a *Applet) Close(ctx context.Context) error {
	return a.mod.Close(ctx)
}

// Run runs the applet once, streaming objects through it:
//
//   - When the applet reads its next input object, Run takes it from input.
//     When input ends, the applet's input ends. If input yields an error, the
//     run stops and Run returns that error. If the applet stops reading early,
//     Run stops input: the iteration's yield returns false.
//   - For each object the applet outputs, Run calls output. If output returns
//     an error, the run stops and Run returns that error.
//   - For each line the applet logs, Run calls log.
//
// input, output and log are called one at a time, and the applet waits while
// they do; nothing is read ahead. While input blocks, ctx cannot stop the
// applet, so input should return when ctx is done. input may be nil for no
// input, and output and log may be nil to discard what the applet outputs or
// logs.
func (a *Applet) Run(ctx context.Context, params json.RawMessage, input iter.Seq2[json.RawMessage, error], output func(json.RawMessage) error, log func(string)) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	if input == nil {
		input = func(func(json.RawMessage, error) bool) {}
	}
	if output == nil {
		output = func(json.RawMessage) error { return nil }
	}
	next, stop := iter.Pull2(input)
	defer stop()

	if len(params) == 0 {
		params = json.RawMessage("null")
	}
	stdin := &inputReader{next: next, cancel: cancel}
	if err := stdin.setLine(params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	stderr := &recordWriter{log: log, cancel: cancel}
	stdout := &outputWriter{output: output, stderr: stderr, cancel: cancel}
	cfg := wazero.NewModuleConfig().
		// Anonymous, so instances of concurrent runs do not clash.
		WithName("").
		WithStartFunctions().
		WithStdin(stdin).
		WithStdout(&lineWriter{emit: stdout.emit}).
		WithStderr(&lineWriter{emit: stderr.emit})

	deps := map[string]api.Module{}
	defer func() {
		for _, mod := range deps {
			mod.Close(context.WithoutCancel(ctx))
		}
	}()
	for name, compiled := range a.e.deps {
		mod, err := a.e.rt.InstantiateModule(ctx, compiled, cfg)
		if err != nil {
			return a.runError(ctx, fmt.Errorf("cannot instantiate dependency %q: %w", name, err), stderr)
		}
		deps[name] = mod
	}
	resolveCtx := experimental.WithImportResolver(ctx, func(name string) api.Module {
		if mod, ok := deps[name]; ok {
			return mod
		}
		return nil
	})
	mod, err := a.e.rt.InstantiateModule(resolveCtx, a.mod, cfg)
	if err != nil {
		return a.runError(ctx, fmt.Errorf("cannot instantiate applet: %w", err), stderr)
	}
	defer mod.Close(context.WithoutCancel(ctx))

	_, err = mod.ExportedFunction("_start").Call(ctx)
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 0 {
		err = nil
	}
	if err != nil {
		return a.runError(ctx, err, stderr)
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if stderr.appletErr != nil {
		return a.classify(stderr.appletErr)
	}
	if !stderr.done {
		return ErrNotDone
	}
	return nil
}

// RunAll runs the applet once on the given input objects, and returns its
// output objects. It holds all input and output in memory at once, so it is
// meant for small runs and tests; Run streams them instead. If the run fails,
// RunAll also returns the output objects from before the failure. It calls
// log, if not nil, for each line the applet logs.
func (a *Applet) RunAll(ctx context.Context, params json.RawMessage, inputs []json.RawMessage, log func(string)) ([]json.RawMessage, error) {
	input := func(yield func(json.RawMessage, error) bool) {
		for _, obj := range inputs {
			if !yield(obj, nil) {
				return
			}
		}
	}
	var outputs []json.RawMessage
	err := a.Run(ctx, params, input, func(obj json.RawMessage) error {
		outputs = append(outputs, obj)
		return nil
	}, log)
	return outputs, err
}

// runError determines the error of a failed run: a cause set by the host
// takes precedence over what the applet reported.
func (a *Applet) runError(ctx context.Context, err error, stderr *recordWriter) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if stderr.appletErr != nil {
		return a.classify(stderr.appletErr)
	}
	if isOutOfMemory(err.Error()) || isOutOfMemory(stderr.lastRaw) {
		return ErrMemory
	}
	if stderr.lastRaw != "" {
		return &AppletError{Message: stderr.lastRaw, Input: -1}
	}
	// A trap without a message from the applet. The first line of the error
	// is the trap; the rest is a wasm stack trace, which means nothing to an
	// applet author.
	msg, _, _ := strings.Cut(err.Error(), "\n")
	if strings.Contains(msg, "out of bounds memory access") {
		// Seen when the wasm stack overflows, e.g. on unbounded recursion.
		msg = "RangeError: stack overflow (" + msg + ")"
	}
	return &AppletError{Message: msg, Input: -1}
}

func (a *Applet) classify(err *AppletError) error {
	if isOutOfMemory(err.Message) {
		return ErrMemory
	}
	return err
}

func isOutOfMemory(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "out of memory") || strings.Contains(msg, "memory allocation")
}
