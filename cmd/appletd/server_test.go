package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skybitsnl/go-applets/appletpb"
	"github.com/skybitsnl/go-applets/build"
	"github.com/skybitsnl/go-applets/runner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// builder and engines are shared by the tests: they take long to create.
var (
	builder *build.Builder
	engines *engineCache
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	builder, err = build.NewBuilder(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	engines = newEngineCache(builder.Dependencies(), 4)
	code := m.Run()
	engines.close()
	if err := builder.Close(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "cannot close builder:", err)
		code = max(code, 1)
	}
	os.Exit(code)
}

func testConfig() config {
	return config{
		maxStreams:         4,
		maxMemoryMiB:       64,
		maxMessageBytes:    minMessageBytes,
		buildCacheEntries:  100,
		engineCacheEntries: 4,
	}
}

// dial starts a server with cfg, and returns a client of it.
func dial(t *testing.T, cfg config) appletpb.AppletsClient {
	t.Helper()
	srv := newServer(cfg, builder)
	srv.engines = engines
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.maxMessageBytes), grpc.MaxSendMsgSize(cfg.maxMessageBytes))
	appletpb.RegisterAppletsServer(gs, srv)
	go gs.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		gs.Stop()
	})
	return appletpb.NewAppletsClient(conn)
}

// anySignature lets tests leave out types. Their sources must still type
// their parameters, as tsc runs in strict mode.
const anySignature = `import("go-applets").Applet<any, any, any>`

const passThrough = `export default function* (input: Iterable<object>) { yield* input; }`

func applet(source string) *appletpb.Applet {
	return &appletpb.Applet{Source: source, Signature: anySignature}
}

func startReq(applets ...*appletpb.Applet) *appletpb.RunRequest {
	return &appletpb.RunRequest{Kind: &appletpb.RunRequest_Start{Start: &appletpb.Start{Applets: applets}}}
}

func objectReq(json string) *appletpb.RunRequest {
	return &appletpb.RunRequest{Kind: &appletpb.RunRequest_Object{Object: &appletpb.Object{Json: []byte(json)}}}
}

// result is what the client received on a stream.
type result struct {
	running bool
	objects []string
	// logs are "applet: line".
	logs []string
	// end is the Done, Error or CompileError.
	end any
	// err is the status the stream ended with, or a violation of the
	// order of messages.
	err error
}

// runStream sends reqs on a new stream and half-closes it, and returns what it
// receives.
func runStream(t *testing.T, client appletpb.AppletsClient, reqs ...*appletpb.RunRequest) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := client.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, req := range reqs {
			if stream.Send(req) != nil {
				return
			}
		}
		stream.CloseSend()
	}()
	return receive(stream)
}

func receive(stream appletpb.Applets_RunClient) result {
	var res result
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return res
		}
		if err != nil {
			res.err = err
			return res
		}
		if res.end != nil {
			res.err = fmt.Errorf("received %v after the end", msg)
			return res
		}
		switch kind := msg.Kind.(type) {
		case *appletpb.RunResponse_Running:
			res.running = true
		case *appletpb.RunResponse_Object:
			res.objects = append(res.objects, string(kind.Object.Json))
		case *appletpb.RunResponse_Log:
			res.logs = append(res.logs, fmt.Sprintf("%d: %s", kind.Log.Applet, kind.Log.Line))
		case *appletpb.RunResponse_Done:
			res.end = kind.Done
		case *appletpb.RunResponse_Error:
			res.end = kind.Error
		case *appletpb.RunResponse_CompileError:
			res.end = kind.CompileError
		}
		if _, ok := msg.Kind.(*appletpb.RunResponse_Running); !ok && !res.running && res.end == nil {
			res.err = fmt.Errorf("received %v before Running", msg)
			return res
		}
	}
}

// runError runs reqs, and returns the Error the stream ended with.
func runError(t *testing.T, client appletpb.AppletsClient, reqs ...*appletpb.RunRequest) *appletpb.Error {
	t.Helper()
	res := runStream(t, client, reqs...)
	e, ok := res.end.(*appletpb.Error)
	if res.err != nil || !ok {
		t.Fatalf("got %v, %v, want an Error", res.end, res.err)
	}
	return e
}

func TestPassThrough(t *testing.T) {
	client := dial(t, testConfig())
	res := runStream(t, client, startReq(applet(passThrough)), objectReq(`{"a":1}`), objectReq(` {"b": [2, 3]}`))
	if res.err != nil {
		t.Fatal(res.err)
	}
	if _, ok := res.end.(*appletpb.Done); !ok || !res.running {
		t.Errorf("got running %v and end %v, want Running and Done", res.running, res.end)
	}
	if want := []string{`{"a":1}`, `{"b":[2,3]}`}; !equal(res.objects, want) {
		t.Errorf("got %q, want %q", res.objects, want)
	}
}

func TestParamsAndDeclarations(t *testing.T) {
	client := dial(t, testConfig())
	start := &appletpb.Start{
		Declarations: `declare module "shop" { export interface Order { total: number } }`,
		Applets: []*appletpb.Applet{{
			Source: `import type { Applet } from "go-applets";
import type { Order } from "shop";
const applet: Applet<Order, Order, { factor: number }> = function* (input, params) {
  for (const order of input) yield { total: order.total * params.factor };
};
export default applet;`,
			Signature: `import("go-applets").Applet<import("shop").Order, import("shop").Order, { factor: number }>`,
			Params:    []byte(`{"factor": 3}`),
		}},
	}
	res := runStream(t, client, &appletpb.RunRequest{Kind: &appletpb.RunRequest_Start{Start: start}}, objectReq(`{"total":2}`))
	if want := []string{`{"total":6}`}; res.err != nil || !equal(res.objects, want) {
		t.Errorf("got %q, %v, want %q", res.objects, res.err, want)
	}
}

func TestLogs(t *testing.T) {
	client := dial(t, testConfig())
	res := runStream(t, client, startReq(applet(`export default function* () {
  console.log("hello", { a: 1 });
  // A raw line that is not valid UTF-8.
  (globalThis as any).Javy.IO.writeSync(2, new Uint8Array([0x61, 0xff, 0x0a]));
}`)))
	if want := []string{`0: hello {"a":1}`, "0: a�"}; res.err != nil || !equal(res.logs, want) {
		t.Errorf("got %q, %v, want %q", res.logs, res.err, want)
	}
}

func TestCompileError(t *testing.T) {
	client := dial(t, testConfig())
	res := runStream(t, client, startReq(applet("export default function* (input: Iterable<object>) {\n  const n: number = \"x\";\n}")))
	compileErr, ok := res.end.(*appletpb.CompileError)
	if res.err != nil || !ok || res.running {
		t.Fatalf("got running %v, %v, %v, want only a CompileError", res.running, res.end, res.err)
	}
	if len(compileErr.Errors) != 1 {
		t.Fatalf("got %v, want one error", compileErr.Errors)
	}
	e := compileErr.Errors[0]
	if e.Applet != 0 || e.Step != appletpb.SourceError_TYPECHECK || e.Line != 2 || e.Code != "TS2322" {
		t.Errorf("got %v, want a type error on line 2", e)
	}
}

func TestAppletFails(t *testing.T) {
	client := dial(t, testConfig())
	e := runError(t, client, startReq(applet(`export default function* (input: Iterable<{ ok: boolean }>) {
  for (const obj of input) {
    if (!obj.ok) throw new Error("not ok");
    yield obj;
  }
}`)), objectReq(`{"ok":true}`), objectReq(`{"ok":false}`))
	if e.Reason != appletpb.Error_APPLET_FAILED || e.Message != "Error: not ok" || e.Input == nil || *e.Input != 1 || e.Stack == "" {
		t.Errorf("got %v, want APPLET_FAILED on input 1 with a stack", e)
	}
}

func TestMemoryLimit(t *testing.T) {
	client := dial(t, testConfig())
	// The applet needs about 8 MiB, which the server allows.
	source := `export default function* () { yield { length: "x".repeat(8 << 20).length }; }`
	if res := runStream(t, client, startReq(applet(source))); res.err != nil || len(res.objects) != 1 {
		t.Errorf("with the server's limit: got %q, %v, %v, want success", res.objects, res.end, res.err)
	}

	start := &appletpb.Start{Applets: []*appletpb.Applet{applet(source)}, Limits: &appletpb.Limits{MemoryMib: 4}}
	e := runError(t, client, &appletpb.RunRequest{Kind: &appletpb.RunRequest_Start{Start: start}})
	if e.Reason != appletpb.Error_OUT_OF_MEMORY {
		t.Errorf("with memory_mib 4: got %v, want OUT_OF_MEMORY", e)
	}
}

func TestInvalidOutput(t *testing.T) {
	client := dial(t, testConfig())
	e := runError(t, client, startReq(applet(`export default function* () {
  // "[]\n", bypassing the prelude's check that output is an object.
  (globalThis as any).Javy.IO.writeSync(1, new Uint8Array([0x5b, 0x5d, 0x0a]));
}`)))
	if e.Reason != appletpb.Error_INVALID_OUTPUT {
		t.Errorf("got %v, want INVALID_OUTPUT", e)
	}
}

func TestLargeOutput(t *testing.T) {
	client := dial(t, testConfig())
	size := minMessageBytes + 1
	res := runStream(t, client, startReq(applet(fmt.Sprintf(`export default function* () {
  console.log("x".repeat(%d));
  yield { s: "x".repeat(%d) };
}`, size, size))))
	e, ok := res.end.(*appletpb.Error)
	if res.err != nil || !ok || e.Reason != appletpb.Error_INVALID_OUTPUT {
		t.Errorf("got %v, %v, want INVALID_OUTPUT", res.end, res.err)
	}
	// The log line is cut short to fit.
	if len(res.logs) != 1 || len(res.logs[0]) >= size || !strings.HasSuffix(res.logs[0], "…") {
		t.Errorf("got %d logs, want one cut short", len(res.logs))
	}
}

func TestInvalidRequests(t *testing.T) {
	cfg := testConfig()
	client := dial(t, cfg)
	withLimits := func(limits *appletpb.Limits) *appletpb.RunRequest {
		return &appletpb.RunRequest{Kind: &appletpb.RunRequest_Start{Start: &appletpb.Start{
			Applets: []*appletpb.Applet{applet(passThrough)},
			Limits:  limits,
		}}}
	}
	tests := []struct {
		name string
		reqs []*appletpb.RunRequest
		want string
	}{
		{"no messages", nil, "ended before Start"},
		{"object first", []*appletpb.RunRequest{objectReq(`{}`)}, "first message must be Start"},
		{"no applets", []*appletpb.RunRequest{startReq()}, "no applets"},
		{"two applets", []*appletpb.RunRequest{startReq(applet(passThrough), applet(passThrough))}, "only one applet"},
		{"no signature", []*appletpb.RunRequest{startReq(&appletpb.Applet{Source: passThrough})}, "no signature"},
		{"invalid params", []*appletpb.RunRequest{startReq(&appletpb.Applet{Source: passThrough, Signature: anySignature, Params: []byte("{")})}, "not valid JSON"},
		{"too much memory", []*appletpb.RunRequest{withLimits(&appletpb.Limits{MemoryMib: 65})}, "exceeds the server's maximum of 64"},
		{"no close on context done", []*appletpb.RunRequest{withLimits(&appletpb.Limits{NoCloseOnContextDone: true})}, "does not allow no_close_on_context_done"},
		{"input not an object", []*appletpb.RunRequest{startReq(applet(passThrough)), objectReq(`{}`), objectReq(`[]`)}, "input object 1 is not a JSON object"},
		{"invalid input", []*appletpb.RunRequest{startReq(applet(passThrough)), objectReq(`{`)}, "input object 0 is not a JSON object"},
		{"second start", []*appletpb.RunRequest{startReq(applet(passThrough)), startReq(applet(passThrough))}, "only objects may follow Start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runStream(t, client, tt.reqs...)
			if status.Code(res.err) != codes.InvalidArgument || !strings.Contains(res.err.Error(), tt.want) {
				t.Errorf("got %v, want InvalidArgument: ...%s...", res.err, tt.want)
			}
		})
	}
}

func TestAllowNoCloseOnContextDone(t *testing.T) {
	cfg := testConfig()
	cfg.allowNoCloseOnContextDone = true
	client := dial(t, cfg)
	start := &appletpb.Start{Applets: []*appletpb.Applet{applet(passThrough)}, Limits: &appletpb.Limits{NoCloseOnContextDone: true}}
	res := runStream(t, client, &appletpb.RunRequest{Kind: &appletpb.RunRequest_Start{Start: start}}, objectReq(`{}`))
	if _, ok := res.end.(*appletpb.Done); !ok || res.err != nil {
		t.Errorf("got %v, %v, want Done", res.end, res.err)
	}
}

// TestCancelStopsApplet checks that cancelling a stream stops an applet that
// loops forever, and frees its place for another stream.
func TestCancelStopsApplet(t *testing.T) {
	cfg := testConfig()
	cfg.maxStreams = 1
	client := dial(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream.Send(startReq(applet(`export default function* () { for (;;) {} }`)))
	stream.CloseSend()
	if msg, err := stream.Recv(); err != nil || msg.GetRunning() == nil {
		t.Fatalf("got %v, %v, want Running", msg, err)
	}

	if res := runStream(t, client, startReq(applet(passThrough))); status.Code(res.err) != codes.ResourceExhausted {
		t.Errorf("while the applet runs: got %v, want ResourceExhausted", res.err)
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := runStream(t, client, startReq(applet(passThrough)))
		if status.Code(res.err) != codes.ResourceExhausted {
			if _, ok := res.end.(*appletpb.Done); !ok || res.err != nil {
				t.Errorf("after cancelling: got %v, %v, want Done", res.end, res.err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancelled applet still runs")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestResult(t *testing.T) {
	s := newServer(testConfig(), builder)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	long := strings.Repeat("x", maxErrorBytes+1)

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want *appletpb.Error
		code codes.Code
	}{
		{"applet failed", nil, &runner.AppletError{Message: "boom", Stack: "at x", Input: 2},
			&appletpb.Error{Reason: appletpb.Error_APPLET_FAILED, Message: "boom", Stack: "at x", Input: new(int64(2))}, codes.OK},
		{"applet failed without input", nil, &runner.AppletError{Message: "a\xff", Input: -1},
			&appletpb.Error{Reason: appletpb.Error_APPLET_FAILED, Message: "a�"}, codes.OK},
		{"long message", nil, &runner.AppletError{Message: long, Input: -1},
			&appletpb.Error{Reason: appletpb.Error_APPLET_FAILED, Message: truncate(long, maxErrorBytes)}, codes.OK},
		{"memory", nil, runner.ErrMemory,
			&appletpb.Error{Reason: appletpb.Error_OUT_OF_MEMORY, Message: runner.ErrMemory.Error()}, codes.OK},
		{"invalid output", nil, runner.ErrInvalidOutput,
			&appletpb.Error{Reason: appletpb.Error_INVALID_OUTPUT, Message: runner.ErrInvalidOutput.Error()}, codes.OK},
		{"output after done", nil, runner.ErrAfterDone,
			&appletpb.Error{Reason: appletpb.Error_INVALID_OUTPUT, Message: runner.ErrAfterDone.Error()}, codes.OK},
		{"not done", nil, runner.ErrNotDone,
			&appletpb.Error{Reason: appletpb.Error_NOT_DONE, Message: runner.ErrNotDone.Error()}, codes.OK},
		{"status", nil, status.Error(codes.InvalidArgument, "bad"), nil, codes.InvalidArgument},
		{"other", nil, errors.New("broken"), nil, codes.Internal},
		{"cancelled", cancelled, runner.ErrNotDone, nil, codes.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			msg, err := s.result(ctx, tt.err)
			if status.Code(err) != tt.code {
				t.Fatalf("got status %v, want %v", err, tt.code)
			}
			if tt.want == nil {
				return
			}
			if got := msg.GetError(); !proto.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if msg, err := s.result(context.Background(), nil); err != nil || msg.GetDone() == nil {
		t.Errorf("without error: got %v, %v, want Done", msg, err)
	}
}

func TestSelfTest(t *testing.T) {
	s := newServer(testConfig(), builder)
	s.engines = engines
	if err := s.selfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcdef", 5, "ab…"},
		// "é" is two bytes, and is not split.
		{"aéé", 4, "a…"},
		// No room for the marker.
		{"abcdef", 1, "a"},
	}
	for _, tt := range tests {
		if got := truncate(tt.s, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}

func equal(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

// TestClient runs applets through the client helpers of appletpb.
func TestClient(t *testing.T) {
	client := dial(t, testConfig())
	ctx := context.Background()
	open := func(source string) (*appletpb.Stream, error) {
		return appletpb.Open(ctx, client, &appletpb.Start{Applets: []*appletpb.Applet{applet(source)}})
	}

	stream, err := open(`export default function* (input: Iterable<{ n: number }>) {
  for (const obj of input) {
    if (obj.n < 0) throw new Error("negative");
    console.log("got", obj.n);
    yield { n: obj.n * 2 };
  }
}`)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	outputs, err := stream.RunAll([]json.RawMessage{json.RawMessage(`{"n":1}`), json.RawMessage(`{"n":-1}`)}, func(applet int, line string) {
		logs = append(logs, fmt.Sprintf("%d: %s", applet, line))
	})
	var appletErr *appletpb.Error
	if !errors.As(err, &appletErr) || err.Error() != "applet 0 failed on input 1: Error: negative" {
		t.Errorf("got %v, want an Error on input 1", err)
	}
	if len(outputs) != 1 || string(outputs[0]) != `{"n":2}` || !equal(logs, []string{"0: got 1"}) {
		t.Errorf("got outputs %s and logs %q", outputs, logs)
	}

	_, err = open("export default function* (input: Iterable<object>) {\n  const n: number = \"x\";\n}")
	var compileErr *appletpb.CompileError
	if !errors.As(err, &compileErr) || !strings.HasPrefix(err.Error(), "applets do not build:\napplet 0: 2:9: TS2322: ") {
		t.Errorf("got %v, want a CompileError", err)
	}
}
