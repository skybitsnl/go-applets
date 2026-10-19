package build

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/skybitsnl/go-applets/javy/v9"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func (b *Builder) initCompiler(ctx context.Context) error {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithCustomSections(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return fmt.Errorf("cannot instantiate wasi: %w", err)
	}
	plugin, err := rt.CompileModule(ctx, javy.Plugin())
	if err != nil {
		rt.Close(ctx)
		return fmt.Errorf("cannot compile javy plugin: %w", err)
	}
	var namespace string
	for _, s := range plugin.CustomSections() {
		if s.Name() == "import_namespace" {
			namespace = string(s.Data())
		}
	}
	if namespace == "" {
		rt.Close(ctx)
		return errors.New("javy plugin has no import namespace")
	}
	b.rt = rt
	b.plugin = plugin
	b.namespace = namespace
	return nil
}

// Compile compiles a single Javascript file, as returned by Bundle, to a
// WebAssembly module that runs it. This, itself, is done within a WASM sandbox
// (running QuickJS). It returns an InvalidSourceError if the script does not
// compile.
//
// See Build for usage instructions.
func (b *Builder) Compile(ctx context.Context, js string) ([]byte, error) {
	// Javascript -> QuickJS bytecode
	bytecode, err := b.compileSource(ctx, js)
	if err != nil {
		return nil, err
	}
	// Put the QuickJS bytecode into a WASM module
	return module(b.namespace, bytecode)
}

// Dependencies returns the dependencies of every applet built by this builder.
// These are used to make them available under the correct name by the runner.
// Today that is one module: the Javy plugin, which contains the JavaScript
// engine. The applet modules themselves only contain bytecode, and cannot run
// without it.
//
// The dependencies are the same for every applet, so pass them once to
// runner.NewEngine and load all applets into that Engine; see Build.
func (b *Builder) Dependencies() map[string][]byte {
	return map[string][]byte{b.namespace: javy.Plugin()}
}

// compileSource compiles JavaScript to QuickJS bytecode, in a fresh instance
// of the plugin.
func (b *Builder) compileSource(ctx context.Context, js string) ([]byte, error) {
	mod, err := b.rt.InstantiateModule(ctx, b.plugin, wazero.NewModuleConfig().
		// Anonymous, so instances do not clash in the runtime's namespace.
		WithName("").
		WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate javy plugin: %w", err)
	}
	defer mod.Close(ctx)

	ptr, err := writeGuest(ctx, mod, []byte(js))
	if err != nil {
		return nil, err
	}
	res, err := mod.ExportedFunction("compile-src").Call(ctx, uint64(ptr), uint64(len(js)))
	if err != nil {
		return nil, fmt.Errorf("cannot compile: %w", err)
	}

	// compile-src returns result<list<u8>, string> in the canonical ABI: a
	// discriminant byte, then pointer and length of the payload.
	ret, ok := mod.Memory().Read(uint32(res[0]), 12)
	if !ok {
		return nil, errors.New("compile-src returned an invalid pointer")
	}
	payload, ok := mod.Memory().Read(binary.LittleEndian.Uint32(ret[4:]), binary.LittleEndian.Uint32(ret[8:]))
	if !ok {
		return nil, errors.New("compile-src returned an invalid payload")
	}
	if ret[0] != 0 {
		return nil, &InvalidSourceError{Step: StepCompile, Errors: []SourceError{{Message: string(payload)}}}
	}
	return bytes.Clone(payload), nil
}

// writeGuest copies p into newly allocated guest memory.
func writeGuest(ctx context.Context, mod api.Module, p []byte) (uint32, error) {
	res, err := mod.ExportedFunction("cabi_realloc").Call(ctx, 0, 0, 1, uint64(len(p)))
	if err != nil {
		return 0, fmt.Errorf("cannot allocate guest memory: %w", err)
	}
	ptr := uint32(res[0])
	if !mod.Memory().Write(ptr, p) {
		return 0, errors.New("cannot write guest memory")
	}
	return ptr, nil
}
