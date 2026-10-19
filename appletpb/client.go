// Package appletpb is the gRPC API of appletd, which builds and runs applets.
// applets.proto describes it, and most of this package is generated from it.
//
// Clients can use the generated AppletsClient directly, or its helpers: Open
// sends the applets and waits until they are built, and Stream.Run streams
// objects through them, much like runner.Applet.Run does in-process. The
// generated CompileError and Error messages implement error.
package appletpb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
)

// errUnexpectedEnd is returned when the server ends a stream without the
// message that should end it.
var errUnexpectedEnd = errors.New("appletpb: the server ended the stream without a result")

// Stream is a stream whose applets the server has built, ready to run once.
type Stream struct {
	stream Applets_RunClient
	ctx    context.Context
	cancel context.CancelCauseFunc
	// sent is closed when the input of Run has stopped. It is nil before
	// Run, and only Run uses it.
	sent chan struct{}
}

// Open opens a stream on client, sends start, and waits until the server has
// built the applets. If they do not build, it returns the *CompileError. If
// the server refuses start, it returns a gRPC status error. The stream ends
// when ctx is done, or after Run or Close.
func Open(ctx context.Context, client AppletsClient, start *Start) (*Stream, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	stream, err := client.Run(ctx)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	// If sending fails, the stream ended, and Recv returns why.
	_ = stream.Send(&RunRequest{Kind: &RunRequest_Start{Start: start}})
	msg, err := stream.Recv()
	if err == io.EOF {
		err = errUnexpectedEnd
	}
	if err != nil {
		cancel(nil)
		return nil, err
	}
	switch kind := msg.Kind.(type) {
	case *RunResponse_Running:
		return &Stream{stream: stream, ctx: ctx, cancel: cancel}, nil
	case *RunResponse_CompileError:
		cancel(nil)
		return nil, kind.CompileError
	default:
		cancel(nil)
		return nil, fmt.Errorf("appletpb: the server sent %T instead of Running", msg.Kind)
	}
}

// Run streams objects through the applets:
//
//   - Run sends the objects of input to the first applet, and ends its
//     input when input ends. The server asks for input while output
//     arrives, so input is called from another goroutine than output and
//     log. If input yields an error, the run stops and Run returns that
//     error. If the run ends before the input does, the iteration's yield
//     returns false.
//   - For each object the last applet outputs, Run calls output. If output
//     returns an error, the run stops and Run returns that error.
//   - For each line an applet logs, Run calls log with the index of the
//     applet in Start.Applets.
//
// output and log are called one at a time. input may be nil for no input,
// and output and log may be nil to discard what the applets output or log.
//
// Run returns nil if the run succeeded, an *Error if an applet failed, and a
// gRPC status error for problems with the request. It returns after input
// has stopped, and ends the stream. Run may be called once.
func (s *Stream) Run(input iter.Seq2[json.RawMessage, error], output func(json.RawMessage) error, log func(applet int, line string)) error {
	if s.sent != nil {
		return errors.New("appletpb: Run was called before")
	}
	if input == nil {
		input = func(func(json.RawMessage, error) bool) {}
	}
	if output == nil {
		output = func(json.RawMessage) error { return nil }
	}
	if log == nil {
		log = func(int, string) {}
	}

	s.sent = make(chan struct{})
	go func() {
		defer close(s.sent)
		s.send(input)
	}()
	err := s.receive(output, log)
	if cause := context.Cause(s.ctx); err != nil && cause != nil {
		// The input failed, the caller's context is done, or Close was
		// called.
		err = cause
	}
	s.cancel(nil)
	<-s.sent
	return err
}

// RunAll runs the applets on the given input objects, and returns their
// output objects. It holds all input and output in memory at once, so it is
// meant for small runs and tests; Run streams them instead. If the run
// fails, RunAll also returns the output objects from before the failure. It
// calls log, if not nil, for each line an applet logs.
func (s *Stream) RunAll(inputs []json.RawMessage, log func(applet int, line string)) ([]json.RawMessage, error) {
	input := func(yield func(json.RawMessage, error) bool) {
		for _, obj := range inputs {
			if !yield(obj, nil) {
				return
			}
		}
	}
	var outputs []json.RawMessage
	err := s.Run(input, func(obj json.RawMessage) error {
		outputs = append(outputs, obj)
		return nil
	}, log)
	return outputs, err
}

// Close ends the stream. A client that only wants to check that the applets
// build can Close the stream instead of running it. Close may also be called
// while Run runs, to stop it.
func (s *Stream) Close() {
	s.cancel(nil)
}

// send sends the objects of input, then half-closes the stream to end the
// input. If the stream fails, receive reports why.
func (s *Stream) send(input iter.Seq2[json.RawMessage, error]) {
	for obj, err := range input {
		if err != nil {
			s.cancel(err)
			return
		}
		if s.stream.Send(&RunRequest{Kind: &RunRequest_Object{Object: &Object{Json: obj}}}) != nil {
			return
		}
	}
	s.stream.CloseSend()
}

// receive handles what the server sends until the run ends.
func (s *Stream) receive(output func(json.RawMessage) error, log func(int, string)) error {
	for {
		msg, err := s.stream.Recv()
		if err == io.EOF {
			return errUnexpectedEnd
		}
		if err != nil {
			return err
		}
		switch kind := msg.Kind.(type) {
		case *RunResponse_Object:
			if err := output(kind.Object.Json); err != nil {
				return err
			}
		case *RunResponse_Log:
			log(int(kind.Log.Applet), kind.Log.Line)
		case *RunResponse_Done:
			return nil
		case *RunResponse_Error:
			return kind.Error
		case nil:
			// A message added to the API after this client was built.
		default:
			return fmt.Errorf("appletpb: the server sent %T while running", msg.Kind)
		}
	}
}

// Error makes a CompileError an error, with one line per SourceError.
func (e *CompileError) Error() string {
	var b strings.Builder
	b.WriteString("applets do not build:")
	for _, se := range e.Errors {
		fmt.Fprintf(&b, "\napplet %d: ", se.Applet)
		if se.Line > 0 {
			fmt.Fprintf(&b, "%d:%d: ", se.Line, se.Column)
		}
		if se.Code != "" {
			fmt.Fprintf(&b, "%s: ", se.Code)
		}
		b.WriteString(se.Message)
	}
	return b.String()
}

// Error makes an Error an error. It leaves out the stack.
func (e *Error) Error() string {
	if e.Input != nil {
		return fmt.Sprintf("applet %d failed on input %d: %s", e.Applet, *e.Input, e.Message)
	}
	return fmt.Sprintf("applet %d failed: %s", e.Applet, e.Message)
}
