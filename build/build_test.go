package build_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/skybitsnl/go-applets/build"
)

// builder is shared by the tests: it takes about a second to create.
var builder *build.Builder

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	builder, err = build.NewBuilder(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	// A failure to clean up may be a bug, so it fails the run.
	if err := builder.Close(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "cannot close builder:", err)
		code = max(code, 1)
	}
	os.Exit(code)
}

var env = build.Env{
	Declarations: `declare module "shop" {
  export interface Order { id: number; totalCents: number }
}`,
	Signature: `import("go-applets").Applet<import("shop").Order>`,
}

// sourceErrors checks that err is an *InvalidSourceError of the given step,
// and returns its source errors.
func sourceErrors(t *testing.T, err error, step build.Step) []build.SourceError {
	t.Helper()
	var invalid *build.InvalidSourceError
	if !errors.As(err, &invalid) {
		t.Fatalf("got error %v, want an *InvalidSourceError", err)
	}
	if invalid.Step != step {
		t.Errorf("got step %d, want %d", invalid.Step, step)
	}
	return invalid.Errors
}

func TestBuild(t *testing.T) {
	wasm, err := builder.Build(context.Background(), `
import type { Applet } from "go-applets";
import type { Order } from "shop";
const applet: Applet<Order> = function* (input) {
  for (const order of input) yield order;
};
export default applet;
`, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(wasm), "\x00asm") {
		t.Error("Build did not return a WebAssembly module")
	}
}

func TestTypeError(t *testing.T) {
	err := builder.Check(context.Background(), `
import type { Applet } from "go-applets";
import type { Order } from "shop";
const applet: Applet<Order> = function* (input) {
  for (const order of input) yield { ...order, totalCents: "free" };
};
export default applet;
`, env)
	d := sourceErrors(t, err, build.StepTypecheck)
	if len(d) != 1 || d[0].Line != 4 || d[0].Code != "TS2322" {
		t.Errorf("got diagnostics %v, want TS2322 on line 4", d)
	}
}

func TestSignatureMismatch(t *testing.T) {
	err := builder.Check(context.Background(), `export default function* (input: Iterable<string>) {}`, env)
	d := sourceErrors(t, err, build.StepTypecheck)
	if len(d) == 0 || !strings.Contains(d[0].Message, "applet signature") {
		t.Errorf("got diagnostics %v, want a signature mismatch", d)
	}
}

func TestImportsAreRefused(t *testing.T) {
	for _, source := range []string{
		`import fs from "fs"; export default function* () { yield fs; }`,
		`import { x } from "./other"; export default function* () { yield x; }`,
	} {
		_, err := build.Bundle(source)
		d := sourceErrors(t, err, build.StepBundle)
		if len(d) != 1 || !strings.Contains(d[0].Message, "cannot import") {
			t.Errorf("%s: got diagnostics %v, want an import error", source, d)
		}
	}
}

func TestTypeOnlyImportsAreRemoved(t *testing.T) {
	if _, err := build.Bundle(`import type { Order } from "shop"; export default function* (input: Iterable<Order>) {}`); err != nil {
		t.Error(err)
	}
}

func TestSyntaxError(t *testing.T) {
	_, err := build.Bundle("export default function* () {\n  yield {;\n}")
	d := sourceErrors(t, err, build.StepBundle)
	if len(d) == 0 || d[0].Line != 2 {
		t.Errorf("got diagnostics %v, want an error on line 2", d)
	}
}

func TestCompileError(t *testing.T) {
	_, err := builder.Compile(context.Background(), "let = ;")
	if d := sourceErrors(t, err, build.StepCompile); len(d) != 1 || d[0].Message == "" {
		t.Errorf("got %v, want one error with a message", d)
	}
}

// TestCompilerOptions checks that tsconfig.json is applied: with the defaults
// of tsc, the DOM would be available and indexing would not be checked.
func TestCompilerOptions(t *testing.T) {
	for source, code := range map[string]string{
		`document.title = "x"; export default function* () {}`:                    "TS2584", // no DOM in lib
		`const xs = [1]; const n: number = xs[0]; export default function* () {}`: "TS2322", // number | undefined
	} {
		err := builder.Check(context.Background(), source, env)
		d := sourceErrors(t, err, build.StepTypecheck)
		if len(d) == 0 || d[0].Code != code {
			t.Errorf("%s: got %v, want %s", source, d, code)
		}
	}
}

// TestConcurrentBuilds checks that concurrent builds on a new Builder are safe
// (run it with -race), and give identical modules. They share the first Check,
// which extracts tsc, and the wazero runtime that Compile uses.
func TestConcurrentBuilds(t *testing.T) {
	b, err := build.NewBuilder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(context.Background()) })

	source := `
import type { Applet } from "go-applets";
import type { Order } from "shop";
const applet: Applet<Order> = function* (input) {
  for (const order of input) yield order;
};
export default applet;
`
	modules := make([][]byte, 4)
	var wg sync.WaitGroup
	for i := range modules {
		wg.Go(func() {
			wasm, err := b.Build(context.Background(), source, env)
			if err != nil {
				t.Error(err)
			}
			modules[i] = wasm
		})
	}
	wg.Wait()

	for i, wasm := range modules {
		if !bytes.HasPrefix(wasm, []byte("\x00asm")) {
			t.Errorf("build %d did not return a WebAssembly module", i)
		} else if !bytes.Equal(wasm, modules[0]) {
			t.Errorf("build %d returned a different module than build 0", i)
		}
	}
}
