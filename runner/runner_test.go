package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skybitsnl/go-applets/build"
	"github.com/skybitsnl/go-applets/runner"
)

// builder and engine are shared by the tests: they take about a second to
// create.
var (
	builder *build.Builder
	engine  *runner.Engine
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	builder, err = build.NewBuilder(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	engine, err = runner.NewEngine(ctx, builder.Dependencies(), runner.Limits{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		builder.Close(ctx)
		os.Exit(1)
	}
	code := m.Run()
	// A failure to clean up may be a bug, so it fails the run.
	if err := errors.Join(engine.Close(ctx), builder.Close(ctx)); err != nil {
		fmt.Fprintln(os.Stderr, "cannot close builder or engine:", err)
		code = max(code, 1)
	}
	os.Exit(code)
}

func load(t *testing.T, source string) *runner.Applet {
	t.Helper()
	return loadInEngine(t, engine, source)
}

func loadJS(t *testing.T, js string) *runner.Applet {
	t.Helper()
	return loadJSInEngine(t, engine, js)
}

func loadInEngine(t *testing.T, engine *runner.Engine, source string) *runner.Applet {
	t.Helper()
	js, err := build.Bundle(source)
	if err != nil {
		t.Fatal(err)
	}
	return loadJSInEngine(t, engine, js)
}

func loadJSInEngine(t *testing.T, engine *runner.Engine, js string) *runner.Applet {
	t.Helper()
	wasm, err := builder.Compile(context.Background(), js)
	if err != nil {
		t.Fatal(err)
	}
	applet, err := engine.Load(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { applet.Close(context.Background()) })
	return applet
}

type result struct {
	outputs []string
	logs    []string
	err     error
}

// run runs an applet on the given input objects, with RunAll.
func run(ctx context.Context, applet *runner.Applet, params string, inputs ...string) result {
	objs := make([]json.RawMessage, len(inputs))
	for i, obj := range inputs {
		objs[i] = json.RawMessage(obj)
	}
	var res result
	outputs, err := applet.RunAll(ctx, json.RawMessage(params), objs, func(line string) {
		res.logs = append(res.logs, line)
	})
	for _, obj := range outputs {
		res.outputs = append(res.outputs, string(obj))
	}
	res.err = err
	return res
}

func equal(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

func TestPassThrough(t *testing.T) {
	applet := load(t, `export default function* (input) { for (const obj of input) yield obj; }`)
	res := run(context.Background(), applet, "", `{"a":1}`, `{"b": [2, 3]}`)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if want := []string{`{"a":1}`, `{"b":[2,3]}`}; !equal(res.outputs, want) {
		t.Errorf("got %v, want %v", res.outputs, want)
	}
}

func TestParamsAndOutputAfterInput(t *testing.T) {
	applet := load(t, `
export default function* (input: Iterable<{ n: number }>, params: { factor: number }) {
  let total = 0;
  for (const obj of input) {
    total += obj.n;
    yield { n: obj.n * params.factor };
  }
  yield { total };
}`)
	res := run(context.Background(), applet, `{"factor": 10}`, `{"n":1}`, `{"n":2}`)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if want := []string{`{"n":10}`, `{"n":20}`, `{"total":3}`}; !equal(res.outputs, want) {
		t.Errorf("got %v, want %v", res.outputs, want)
	}
}

func TestLogs(t *testing.T) {
	applet := load(t, `
export default function* (input) {
  console.log("hello", 1, { a: 2 });
  for (const obj of input) console.error("got", obj);
}`)
	res := run(context.Background(), applet, "", `{"x":1}`)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if want := []string{`hello 1 {"a":2}`, `got {"x":1}`}; !equal(res.logs, want) {
		t.Errorf("got logs %q, want %q", res.logs, want)
	}
}

func TestThrow(t *testing.T) {
	applet := load(t, `
export default function* (input: Iterable<{ ok: boolean }>) {
  for (const obj of input) {
    if (!obj.ok) throw new Error("not ok");
    yield obj;
  }
}`)
	res := run(context.Background(), applet, "", `{"ok":true}`, `{"ok":false}`, `{"ok":true}`)
	var appletErr *runner.AppletError
	if !errors.As(res.err, &appletErr) {
		t.Fatalf("got %v, want an AppletError", res.err)
	}
	if appletErr.Message != "Error: not ok" || appletErr.Input != 1 {
		t.Errorf("got %q on input %d, want \"Error: not ok\" on input 1", appletErr.Message, appletErr.Input)
	}
	// Output before the failure has been sent already.
	if want := []string{`{"ok":true}`}; !equal(res.outputs, want) {
		t.Errorf("got %v, want %v", res.outputs, want)
	}
}

func TestOutputMustBeObject(t *testing.T) {
	// The prelude refuses non-objects...
	applet := load(t, `export default function* () { yield 42; }`)
	var appletErr *runner.AppletError
	if res := run(context.Background(), applet, ""); !errors.As(res.err, &appletErr) {
		t.Errorf("got %v, want an AppletError", res.err)
	}

	// ...and so does the runner, for applets that bypass it.
	applet = load(t, `
export default function* () {
  (globalThis as any).Javy.IO.writeSync(1, new TextEncoder().encode("42\n"));
}`)
	if res := run(context.Background(), applet, ""); !errors.Is(res.err, runner.ErrInvalidOutput) {
		t.Errorf("got %v, want ErrInvalidOutput", res.err)
	}
}

func TestMustReportDone(t *testing.T) {
	applet := loadJS(t, `const x = 1;`)
	if res := run(context.Background(), applet, ""); !errors.Is(res.err, runner.ErrNotDone) {
		t.Errorf("got %v, want ErrNotDone", res.err)
	}
}

func TestNoOutputAfterDone(t *testing.T) {
	applet := loadJS(t, `
const enc = new TextEncoder();
Javy.IO.writeSync(2, enc.encode('{"done":true}\n'));
Javy.IO.writeSync(1, enc.encode('{}\n'));
`)
	if res := run(context.Background(), applet, ""); !errors.Is(res.err, runner.ErrAfterDone) {
		t.Errorf("got %v, want ErrAfterDone", res.err)
	}
}

func TestContextStopsInfiniteLoop(t *testing.T) {
	applet := load(t, `export default function* () { for (;;) {} }`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := run(ctx, applet, "")
	if !errors.Is(res.err, context.DeadlineExceeded) {
		t.Errorf("got %v, want DeadlineExceeded", res.err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v to stop", d)
	}
}

func TestConcurrentRuns(t *testing.T) {
	applet := load(t, `
export default function* (input: Iterable<{ n: number }>) {
  for (const obj of input) yield { n: obj.n + 1 };
}`)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			res := run(context.Background(), applet, "", `{"n":`+string(rune('0'+i))+`}`)
			if want := []string{`{"n":` + string(rune('1'+i)) + `}`}; res.err != nil || !equal(res.outputs, want) {
				t.Errorf("run %d: got %v, %v, want %v", i, res.outputs, res.err, want)
			}
		})
	}
	wg.Wait()
}

func TestEndToEnd(t *testing.T) {
	env := build.Env{
		Declarations: `declare module "shop" {
  export interface Order { id: number; totalCents: number }
}`,
		Signature: `import("go-applets").Applet<import("shop").Order, import("shop").Order, { surchargePercent: number }>`,
	}
	wasm, err := builder.Build(context.Background(), `
import type { Applet } from "go-applets";
import type { Order } from "shop";

const applet: Applet<Order, Order, { surchargePercent: number }> = function* (input, params) {
  for (const order of input) {
    yield { ...order, totalCents: Math.round(order.totalCents * (1 + params.surchargePercent / 100)) };
  }
};
export default applet;
`, env)
	if err != nil {
		t.Fatal(err)
	}
	applet, err := engine.Load(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	defer applet.Close(context.Background())
	res := run(context.Background(), applet, `{"surchargePercent": 5}`, `{"id":1,"totalCents":1000}`, `{"id":2,"totalCents":250}`)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if want := []string{`{"id":1,"totalCents":1050}`, `{"id":2,"totalCents":263}`}; !equal(res.outputs, want) {
		t.Errorf("got %v, want %v", res.outputs, want)
	}
}

func TestInputError(t *testing.T) {
	applet := load(t, `export default function* (input) { for (const obj of input) yield obj; }`)
	errRead := errors.New("cannot read input")
	input := func(yield func(json.RawMessage, error) bool) {
		if yield(json.RawMessage(`{"a":1}`), nil) {
			yield(nil, errRead)
		}
	}
	var outputs []string
	err := applet.Run(context.Background(), nil, input, func(obj json.RawMessage) error {
		outputs = append(outputs, string(obj))
		return nil
	}, nil)
	if !errors.Is(err, errRead) {
		t.Errorf("got %v, want the error of input", err)
	}
	if want := []string{`{"a":1}`}; !equal(outputs, want) {
		t.Errorf("got %v, want %v", outputs, want)
	}
}

func TestOutputError(t *testing.T) {
	applet := load(t, `export default function* (input) { for (const obj of input) yield obj; }`)
	errWrite := errors.New("cannot write output")
	outputs := 0
	input := func(yield func(json.RawMessage, error) bool) {
		for range 3 {
			if !yield(json.RawMessage(`{}`), nil) {
				return
			}
		}
	}
	err := applet.Run(context.Background(), nil, input, func(json.RawMessage) error {
		outputs++
		return errWrite
	}, nil)
	if !errors.Is(err, errWrite) {
		t.Errorf("got %v, want the error of output", err)
	}
	if outputs != 1 {
		t.Errorf("output was called %d times, want once", outputs)
	}
}

func TestStopsInputWhenAppletStopsReading(t *testing.T) {
	applet := load(t, `
export default function* (input) {
  for (const obj of input) {
    yield obj;
    return;
  }
}`)
	offered, stopped := 0, false
	input := func(yield func(json.RawMessage, error) bool) {
		defer func() { stopped = true }()
		for range 100 {
			offered++
			if !yield(json.RawMessage(`{}`), nil) {
				return
			}
		}
	}
	if err := applet.Run(context.Background(), nil, input, nil, nil); err != nil {
		t.Fatal(err)
	}
	if offered != 1 || !stopped {
		t.Errorf("input offered %d objects and stopped=%v, want 1 object and stopped", offered, stopped)
	}
}
