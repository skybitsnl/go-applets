package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/skybitsnl/go-applets/appletpb"
	"github.com/skybitsnl/go-applets/build"
	"github.com/skybitsnl/go-applets/runner"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// config is the configuration of the server, from the command line.
type config struct {
	maxStreams                int
	maxMemoryMiB              uint32
	allowNoCloseOnContextDone bool
	maxMessageBytes           int
	buildCacheEntries         int
	engineCacheEntries        int
}

// maxErrorBytes bounds the message and the stack of an Error, which come
// from the applet.
const maxErrorBytes = 16 << 10

// server implements the Applets service.
type server struct {
	cfg     config
	builds  *buildCache
	engines *engineCache
	// streams has a value for each stream that runs.
	streams chan struct{}
}

func newServer(cfg config, builder *build.Builder) *server {
	return &server{
		cfg:     cfg,
		builds:  newBuildCache(builder, cfg.buildCacheEntries),
		engines: newEngineCache(builder.Dependencies(), cfg.engineCacheEntries),
		streams: make(chan struct{}, cfg.maxStreams),
	}
}

// close releases the engines. Streams must have ended.
func (s *server) close() {
	s.engines.close()
}

// Run implements appletpb.AppletsServer.
func (s *server) Run(stream appletpb.Applets_RunServer) error {
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		return status.Errorf(codes.ResourceExhausted, "the server runs its maximum of %d streams", s.cfg.maxStreams)
	}
	ctx := stream.Context()

	req, err := stream.Recv()
	if err == io.EOF {
		return status.Error(codes.InvalidArgument, "the stream ended before Start")
	} else if err != nil {
		return err
	}
	start := req.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "the first message must be Start")
	}
	key, err := s.validate(start)
	if err != nil {
		return err
	}

	wasms, compileErr, err := s.build(ctx, start)
	if err != nil {
		return s.statusError(ctx, err)
	}
	if compileErr != nil {
		return stream.Send(&appletpb.RunResponse{Kind: &appletpb.RunResponse_CompileError{CompileError: compileErr}})
	}

	engine, release, err := s.engines.acquire(ctx, key)
	if err != nil {
		return s.statusError(ctx, err)
	}
	defer release()
	applet, err := engine.Load(ctx, wasms[0])
	if err != nil {
		return s.statusError(ctx, err)
	}
	defer applet.Close(context.WithoutCancel(ctx))

	if err := stream.Send(&appletpb.RunResponse{Kind: &appletpb.RunResponse_Running{Running: &appletpb.Running{}}}); err != nil {
		return err
	}
	output := func(obj json.RawMessage) error {
		msg := &appletpb.RunResponse{Kind: &appletpb.RunResponse_Object{Object: &appletpb.Object{Json: obj}}}
		if size := proto.Size(msg); size > s.cfg.maxMessageBytes {
			return &outputTooLargeError{size: size, max: s.cfg.maxMessageBytes}
		}
		return stream.Send(msg)
	}
	log := func(line string) {
		// The applet does not wait for the client to receive its logs, so
		// an error here shows up in the next output or the end of the run.
		_ = stream.Send(s.logMessage(0, line))
	}
	err = applet.Run(ctx, start.Applets[0].Params, input(stream), output, log)

	msg, err := s.result(ctx, err)
	if err != nil {
		return err
	}
	return stream.Send(msg)
}

// validate checks Start, and returns the key of the engine to run it in.
func (s *server) validate(start *appletpb.Start) (engineKey, error) {
	invalid := func(format string, args ...any) (engineKey, error) {
		return engineKey{}, status.Errorf(codes.InvalidArgument, format, args...)
	}
	switch n := len(start.Applets); {
	case n == 0:
		return invalid("Start has no applets")
	case n > 1:
		// TODO: run pipelines.
		return invalid("this server runs only one applet per stream")
	}
	for i, applet := range start.Applets {
		if applet.Signature == "" {
			return invalid("applet %d has no signature", i)
		}
		if len(applet.Params) > 0 && !json.Valid(applet.Params) {
			return invalid("the params of applet %d are not valid JSON", i)
		}
	}

	key := engineKey{memoryMiB: s.cfg.maxMemoryMiB, closeOnContextDone: true}
	if mib := start.GetLimits().GetMemoryMib(); mib > s.cfg.maxMemoryMiB {
		return invalid("memory_mib %d exceeds the server's maximum of %d", mib, s.cfg.maxMemoryMiB)
	} else if mib > 0 {
		key.memoryMiB = mib
	}
	if start.GetLimits().GetNoCloseOnContextDone() {
		if !s.cfg.allowNoCloseOnContextDone {
			return invalid("the server does not allow no_close_on_context_done")
		}
		key.closeOnContextDone = false
	}
	return key, nil
}

// build builds every applet in start. If any does not build, it returns the
// errors of all applets that do not build.
func (s *server) build(ctx context.Context, start *appletpb.Start) ([][]byte, *appletpb.CompileError, error) {
	var wasms [][]byte
	var compileErr *appletpb.CompileError
	for i, applet := range start.Applets {
		wasm, err := s.builds.build(ctx, applet.Source, build.Env{Declarations: start.Declarations, Signature: applet.Signature})
		if invalid, ok := errors.AsType[*build.InvalidSourceError](err); ok {
			if compileErr == nil {
				compileErr = &appletpb.CompileError{}
			}
			for _, e := range invalid.Errors {
				compileErr.Errors = append(compileErr.Errors, &appletpb.SourceError{
					Applet:  int32(i),
					Step:    sourceErrorStep(invalid.Step),
					Line:    int32(e.Line),
					Column:  int32(e.Column),
					Code:    e.Code,
					Message: strings.ToValidUTF8(e.Message, "�"),
				})
			}
		} else if err != nil {
			return nil, nil, err
		}
		wasms = append(wasms, wasm)
	}
	return wasms, compileErr, nil
}

func sourceErrorStep(step build.Step) appletpb.SourceError_Step {
	switch step {
	case build.StepTypecheck:
		return appletpb.SourceError_TYPECHECK
	case build.StepBundle:
		return appletpb.SourceError_BUNDLE
	case build.StepCompile:
		return appletpb.SourceError_COMPILE
	}
	return appletpb.SourceError_STEP_UNSPECIFIED
}

// input returns the objects the client sends after Start, until it
// half-closes the stream.
func input(stream appletpb.Applets_RunServer) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		for i := 0; ; i++ {
			req, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			obj := req.GetObject()
			if obj == nil {
				yield(nil, status.Error(codes.InvalidArgument, "only objects may follow Start"))
				return
			}
			if !isJSONObject(obj.Json) {
				yield(nil, status.Errorf(codes.InvalidArgument, "input object %d is not a JSON object", i))
				return
			}
			if !yield(obj.Json, nil) {
				return
			}
		}
	}
}

func isJSONObject(b []byte) bool {
	b = bytes.TrimLeft(b, " \t\r\n")
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}

// logMessage returns a Log message, with the line cut short if the message
// would be too large.
func (s *server) logMessage(applet int, line string) *appletpb.RunResponse {
	log := &appletpb.Log{Applet: int32(applet), Line: strings.ToValidUTF8(line, "�")}
	msg := &appletpb.RunResponse{Kind: &appletpb.RunResponse_Log{Log: log}}
	if over := proto.Size(msg) - s.cfg.maxMessageBytes; over > 0 {
		log.Line = truncate(log.Line, len(log.Line)-over)
	}
	return msg
}

// outputTooLargeError is returned when an output object does not fit in a
// message.
type outputTooLargeError struct {
	size, max int
}

func (e *outputTooLargeError) Error() string {
	return fmt.Sprintf("applet output an object of %d bytes, more than the maximum of %d", e.size, e.max)
}

// result returns the message that ends a run that returned err, or the error
// to end the stream with.
func (s *server) result(ctx context.Context, err error) (*appletpb.RunResponse, error) {
	if err == nil {
		return &appletpb.RunResponse{Kind: &appletpb.RunResponse_Done{Done: &appletpb.Done{}}}, nil
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if _, ok := status.FromError(err); ok {
		// From the client's side of the stream, e.g. invalid input.
		return nil, err
	}

	e := &appletpb.Error{Applet: 0, Message: err.Error()}
	var appletErr *runner.AppletError
	var tooLarge *outputTooLargeError
	switch {
	case errors.As(err, &appletErr):
		e.Reason = appletpb.Error_APPLET_FAILED
		e.Message = appletErr.Message
		e.Stack = appletErr.Stack
		if appletErr.Input >= 0 {
			e.Input = new(int64(appletErr.Input))
		}
	case errors.Is(err, runner.ErrMemory):
		e.Reason = appletpb.Error_OUT_OF_MEMORY
	case errors.Is(err, runner.ErrInvalidOutput), errors.Is(err, runner.ErrAfterDone), errors.As(err, &tooLarge):
		e.Reason = appletpb.Error_INVALID_OUTPUT
	case errors.Is(err, runner.ErrNotDone):
		e.Reason = appletpb.Error_NOT_DONE
	default:
		return nil, s.statusError(ctx, err)
	}
	e.Message = truncate(strings.ToValidUTF8(e.Message, "�"), maxErrorBytes)
	e.Stack = truncate(strings.ToValidUTF8(e.Stack, "�"), maxErrorBytes)
	return &appletpb.RunResponse{Kind: &appletpb.RunResponse_Error{Error: e}}, nil
}

// statusError returns the status to end a stream with after an error that
// the client cannot do anything about, unless the stream was cancelled.
func (s *server) statusError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	slog.Error("stream failed", "error", err)
	return status.Error(codes.Internal, err.Error())
}

// truncate cuts s short to at most n bytes, without splitting a character,
// and marks that it did so if there is room for the marker.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	marker := "…"
	if n < len(marker) {
		marker = ""
	}
	n -= len(marker)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + marker
}
