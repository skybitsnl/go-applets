package build

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/skybitsnl/go-tsc/v7"
)

//go:embed runtime.d.ts
var runtimeDeclarations string

//go:embed tsconfig.json
var baseConfig string

// Env describes what an applet is checked against.
type Env struct {
	// Declarations is the content of a .d.ts file with the ambient modules
	// an applet may import types from, e.g. `declare module "invoice" {...}`.
	Declarations string
	// Signature is a type the default export of the applet must be
	// assignable to, e.g. `import("go-applets").Applet<import("invoice").Shipment>`.
	// See runtime.d.ts for the Applet type.
	Signature string
}

// The applet is checked in an empty temporary directory with these files. The
// compiler options are in baseConfigFile, a copy of tsconfig.json, which the
// tsconfig.json of the check extends.
const (
	appletFile     = "applet.ts"
	runtimeFile    = "runtime.d.ts"
	envFile        = "env.d.ts"
	checkFile      = "check.ts"
	baseConfigFile = "tsconfig.base.json"
)

// maxOutputBytes bounds the tsc output that is kept. An applet with many
// errors only needs to see the first ones.
const maxOutputBytes = 64 << 10

// typechecker extracts tsc on first use, into a temporary directory that close
// removes. Its zero value is ready to use.
type typechecker struct {
	once sync.Once
	// dir holds tsc and the files it needs next to it.
	dir  string
	path string
	err  error
}

// tsc returns the path of tsc, extracting it the first time.
func (t *typechecker) tsc() (string, error) {
	t.once.Do(func() {
		dir, err := os.MkdirTemp("", "go-applets-tsc-")
		if err != nil {
			t.err = err
			return
		}
		path, err := tsc.Extract(dir)
		if err != nil {
			os.RemoveAll(dir)
			t.err = fmt.Errorf("cannot extract tsc: %w", err)
			return
		}
		t.dir = dir
		t.path = filepath.Join(dir, path)
	})
	return t.path, t.err
}

// close removes the directory with tsc, if tsc was extracted.
func (t *typechecker) close() error {
	if t.dir == "" {
		return nil
	}
	return os.RemoveAll(t.dir)
}

// Check type-checks the applet source with tsc. It returns an InvalidSourceError
// if the source has type errors.
//
// TODO: run tsc with resource limits, so that hostile source can't take
// infinite time and memory.
func (b *Builder) Check(ctx context.Context, source string, env Env) error {
	tscPath, err := b.typechecker.tsc()
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "go-applets-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	tsconfig, err := json.Marshal(map[string]any{
		"extends": "./" + baseConfigFile,
		"files":   []string{appletFile, runtimeFile, envFile, checkFile},
	})
	if err != nil {
		return err
	}
	check := fmt.Sprintf("import applet from %q;\nconst signature: %s = applet;\n", "./applet", env.Signature)
	files := map[string]string{
		"tsconfig.json": string(tsconfig),
		baseConfigFile:  baseConfig,
		appletFile:      source,
		runtimeFile:     runtimeDeclarations,
		envFile:         env.Declarations,
		checkFile:       check,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}

	cmd := exec.CommandContext(ctx, tscPath, "-p", "tsconfig.json", "--pretty", "false", "--singleThreaded")
	cmd.Dir = dir
	cmd.Env = []string{"GOMAXPROCS=1"}
	out := &cappedBuffer{max: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return err
	}

	diags := parse(out.String())
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return fmt.Errorf("cannot run tsc: %w", runErr)
	}
	if len(diags) > 0 {
		return &InvalidSourceError{Step: StepTypecheck, Errors: diags}
	}
	if runErr != nil {
		return fmt.Errorf("tsc failed: %s", strings.TrimSpace(out.String()))
	}
	return nil
}

var diagnosticRe = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\): error (TS\d+): (.*)$`)
var globalDiagnosticRe = regexp.MustCompile(`^error (TS\d+): (.*)$`)

// parse parses the non-pretty output of tsc. Diagnostics in the generated
// check file are reported as a mismatch of the applet's default export.
// TODO: ideally this should move to go-tsc
func parse(out string) []SourceError {
	var diags []SourceError
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if m := diagnosticRe.FindStringSubmatch(line); m != nil {
			d := SourceError{Code: m[4], Message: m[5]}
			switch m[1] {
			case appletFile:
				d.Line, _ = strconv.Atoi(m[2])
				d.Column, _ = strconv.Atoi(m[3])
			case checkFile:
				d.Message = "the default export does not match the applet signature: " + d.Message
			default:
				d.Message = m[1] + ": " + d.Message
			}
			diags = append(diags, d)
		} else if m := globalDiagnosticRe.FindStringSubmatch(line); m != nil {
			diags = append(diags, SourceError{Code: m[1], Message: m[2]})
		} else if len(diags) > 0 && strings.HasPrefix(line, "  ") {
			// Continuation of the previous message.
			diags[len(diags)-1].Message += "\n" + strings.TrimSpace(line)
		}
	}
	return diags
}

// cappedBuffer keeps the first max bytes written to it.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
