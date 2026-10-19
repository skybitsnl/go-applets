package appletpb_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/skybitsnl/go-applets/appletpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// fakeServer runs each stream with run, so a test can script the server.
type fakeServer struct {
	run func(appletpb.Applets_RunServer) error
}

func (f fakeServer) Run(stream appletpb.Applets_RunServer) error {
	return f.run(stream)
}

// dial starts a fake server that runs each stream with run, and returns a
// client of it.
func dial(t *testing.T, run func(appletpb.Applets_RunServer) error) appletpb.AppletsClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	appletpb.RegisterAppletsServer(gs, fakeServer{run: run})
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

var start = &appletpb.Start{Applets: []*appletpb.Applet{{Source: "source", Signature: "signature"}}}

func running() *appletpb.RunResponse {
	return &appletpb.RunResponse{Kind: &appletpb.RunResponse_Running{Running: &appletpb.Running{}}}
}

func done() *appletpb.RunResponse {
	return &appletpb.RunResponse{Kind: &appletpb.RunResponse_Done{Done: &appletpb.Done{}}}
}

// echo is a server that logs which input object it got from applet 1, and
// outputs it, until the input ends. If failOn is not negative, the applet
// fails on that input object instead.
func echo(failOn int) func(appletpb.Applets_RunServer) error {
	return func(stream appletpb.Applets_RunServer) error {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		if !proto.Equal(req.GetStart(), start) {
			return status.Errorf(codes.InvalidArgument, "got %v, want Start", req)
		}
		stream.Send(running())
		for i := 0; ; i++ {
			req, err := stream.Recv()
			if err == io.EOF {
				return stream.Send(done())
			}
			if err != nil {
				return err
			}
			if i == failOn {
				return stream.Send(&appletpb.RunResponse{Kind: &appletpb.RunResponse_Error{Error: &appletpb.Error{
					Reason: appletpb.Error_APPLET_FAILED, Message: "Error: boom", Input: new(int64(i)),
				}}})
			}
			stream.Send(&appletpb.RunResponse{Kind: &appletpb.RunResponse_Log{Log: &appletpb.Log{Applet: 1, Line: fmt.Sprintf("got %d", i)}}})
			stream.Send(&appletpb.RunResponse{Kind: &appletpb.RunResponse_Object{Object: req.GetObject()}})
		}
	}
}

// waitForCancel is a server that sends Running, then reports on cancelled
// when the client cancels the stream.
func waitForCancel(cancelled chan<- struct{}) func(appletpb.Applets_RunServer) error {
	return func(stream appletpb.Applets_RunServer) error {
		stream.Recv()
		stream.Send(running())
		<-stream.Context().Done()
		close(cancelled)
		return nil
	}
}

func objects(objs ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, obj := range objs {
		out = append(out, json.RawMessage(obj))
	}
	return out
}

func equal(a []json.RawMessage, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != b[i] {
			return false
		}
	}
	return true
}

func open(t *testing.T, client appletpb.AppletsClient) *appletpb.Stream {
	t.Helper()
	stream, err := appletpb.Open(context.Background(), client, start)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func expectCancel(t *testing.T, cancelled <-chan struct{}) {
	t.Helper()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Error("the server did not see the stream cancelled")
	}
}

func TestRun(t *testing.T) {
	stream := open(t, dial(t, echo(-1)))
	var outputs []json.RawMessage
	var logs []string
	input := func(yield func(json.RawMessage, error) bool) {
		for _, obj := range objects(`{"a":1}`, `{"b":2}`) {
			if !yield(obj, nil) {
				return
			}
		}
	}
	err := stream.Run(input, func(obj json.RawMessage) error {
		outputs = append(outputs, obj)
		return nil
	}, func(applet int, line string) {
		logs = append(logs, fmt.Sprintf("%d: %s", applet, line))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(outputs, `{"a":1}`, `{"b":2}`) {
		t.Errorf("got outputs %s", outputs)
	}
	if want := "1: got 0,1: got 1"; strings.Join(logs, ",") != want {
		t.Errorf("got logs %q, want %q", logs, want)
	}
	if err := stream.Run(nil, nil, nil); err == nil {
		t.Error("a second Run succeeded")
	}
}

func TestRunAll(t *testing.T) {
	outputs, err := open(t, dial(t, echo(-1))).RunAll(objects(`{"a":1}`, `{"b":2}`), nil)
	if err != nil || !equal(outputs, `{"a":1}`, `{"b":2}`) {
		t.Errorf("got %s, %v", outputs, err)
	}
}

func TestCompileError(t *testing.T) {
	client := dial(t, func(stream appletpb.Applets_RunServer) error {
		stream.Recv()
		return stream.Send(&appletpb.RunResponse{Kind: &appletpb.RunResponse_CompileError{CompileError: &appletpb.CompileError{
			Errors: []*appletpb.SourceError{
				{Applet: 0, Step: appletpb.SourceError_TYPECHECK, Line: 5, Column: 11, Code: "TS2322", Message: "Type 'string' is not assignable to type 'number'."},
				{Applet: 1, Step: appletpb.SourceError_BUNDLE, Message: "Unexpected end of file"},
			},
		}}})
	})
	_, err := appletpb.Open(context.Background(), client, start)
	var compileErr *appletpb.CompileError
	if !errors.As(err, &compileErr) || len(compileErr.Errors) != 2 {
		t.Fatalf("got %v, want a CompileError with two errors", err)
	}
	want := `applets do not build:
applet 0: 5:11: TS2322: Type 'string' is not assignable to type 'number'.
applet 1: Unexpected end of file`
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestAppletError(t *testing.T) {
	outputs, err := open(t, dial(t, echo(1))).RunAll(objects(`{"a":1}`, `{"b":2}`, `{"c":3}`), nil)
	var appletErr *appletpb.Error
	if !errors.As(err, &appletErr) || appletErr.Reason != appletpb.Error_APPLET_FAILED {
		t.Fatalf("got %v, want an Error", err)
	}
	if want := "applet 0 failed on input 1: Error: boom"; err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if !equal(outputs, `{"a":1}`) {
		t.Errorf("got outputs %s, want those from before the failure", outputs)
	}
	if want := "applet 2 failed: x"; (&appletpb.Error{Applet: 2, Message: "x"}).Error() != want {
		t.Errorf("without input: got %q, want %q", (&appletpb.Error{Applet: 2, Message: "x"}).Error(), want)
	}
}

func TestRefusedStart(t *testing.T) {
	client := dial(t, func(stream appletpb.Applets_RunServer) error {
		return status.Error(codes.InvalidArgument, "no")
	})
	if _, err := appletpb.Open(context.Background(), client, start); status.Code(err) != codes.InvalidArgument {
		t.Errorf("got %v, want InvalidArgument", err)
	}
}

func TestInputError(t *testing.T) {
	cancelled := make(chan struct{})
	stream := open(t, dial(t, waitForCancel(cancelled)))
	errInput := errors.New("cannot read input")
	input := func(yield func(json.RawMessage, error) bool) {
		if yield(json.RawMessage(`{}`), nil) {
			yield(nil, errInput)
		}
	}
	if err := stream.Run(input, nil, nil); !errors.Is(err, errInput) {
		t.Errorf("got %v, want the input's error", err)
	}
	expectCancel(t, cancelled)
}

func TestOutputError(t *testing.T) {
	stream := open(t, dial(t, echo(-1)))
	errOutput := errors.New("cannot write output")
	inputStopped := false
	input := func(yield func(json.RawMessage, error) bool) {
		for yield(json.RawMessage(`{}`), nil) {
		}
		inputStopped = true
	}
	err := stream.Run(input, func(json.RawMessage) error { return errOutput }, nil)
	if !errors.Is(err, errOutput) {
		t.Errorf("got %v, want the output's error", err)
	}
	if !inputStopped {
		t.Error("Run returned before the input stopped")
	}
}

func TestUnexpectedEnd(t *testing.T) {
	client := dial(t, func(stream appletpb.Applets_RunServer) error {
		stream.Recv()
		return stream.Send(running())
	})
	if err := open(t, client).Run(nil, nil, nil); err == nil || !strings.Contains(err.Error(), "without a result") {
		t.Errorf("got %v, want an error about the missing result", err)
	}
}

func TestUnknownMessage(t *testing.T) {
	client := dial(t, func(stream appletpb.Applets_RunServer) error {
		stream.Recv()
		stream.Send(running())
		// Without a kind, as a client sees a message it does not know.
		stream.Send(&appletpb.RunResponse{})
		return stream.Send(done())
	})
	if err := open(t, client).Run(nil, nil, nil); err != nil {
		t.Errorf("got %v, want the unknown message ignored", err)
	}
}

func TestClose(t *testing.T) {
	cancelled := make(chan struct{})
	open(t, dial(t, waitForCancel(cancelled))).Close()
	expectCancel(t, cancelled)
}

func TestCloseStopsRun(t *testing.T) {
	cancelled := make(chan struct{})
	stream := open(t, dial(t, waitForCancel(cancelled)))
	time.AfterFunc(50*time.Millisecond, stream.Close)
	if err := stream.Run(nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want Canceled", err)
	}
	expectCancel(t, cancelled)
}

func TestContextStopsRun(t *testing.T) {
	cancelled := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stream, err := appletpb.Open(ctx, dial(t, waitForCancel(cancelled)), start)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Run(nil, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want DeadlineExceeded", err)
	}
	expectCancel(t, cancelled)
}
