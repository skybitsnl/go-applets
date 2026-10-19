// Command appletd is a gRPC service that builds and runs applets, as
// described in package appletpb.
//
// Before it accepts streams, appletd builds and runs a trivial applet, so
// that a broken installation fails at startup, and the first stream is not
// slowed down by extracting tsc and compiling the JavaScript engine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skybitsnl/go-applets/appletpb"
	"github.com/skybitsnl/go-applets/build"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

var (
	listen                    = flag.String("listen", "localhost:2080", "the `address` to listen on")
	maxStreams                = flag.Int("max-streams", 16, "the maximum number of streams running at once")
	maxMemoryMiB              = flag.Uint("max-memory-mib", 64, "the maximum size of each linear memory of an applet, in MiB")
	allowNoCloseOnContextDone = flag.Bool("allow-no-close-on-context-done", false, "allow streams to set no_close_on_context_done, which weakens the sandbox")
	maxMessageBytes           = flag.Int("max-message-bytes", 4<<20, "the maximum size of a gRPC message, which bounds Start and every object")
	buildCacheEntries         = flag.Int("build-cache-entries", 1000, "the number of built applets to keep")
	engineCacheEntries        = flag.Int("engine-cache-entries", 4, "the number of idle engines to keep, one per combination of limits")
	shutdownTimeout           = flag.Duration("shutdown-timeout", 25*time.Second, "how long to wait for streams to end when shutting down")
)

func main() {
	flag.Parse()
	cfg := config{
		maxStreams:                *maxStreams,
		maxMemoryMiB:              uint32(min(*maxMemoryMiB, 4096)),
		allowNoCloseOnContextDone: *allowNoCloseOnContextDone,
		maxMessageBytes:           *maxMessageBytes,
		buildCacheEntries:         *buildCacheEntries,
		engineCacheEntries:        *engineCacheEntries,
	}
	if err := run(*listen, cfg, *shutdownTimeout); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

// minMessageBytes is the smallest -max-message-bytes, so that error messages
// and the errors of a type check always fit in a message.
const minMessageBytes = 1 << 20

func run(listen string, cfg config, shutdownTimeout time.Duration) error {
	switch {
	case cfg.maxStreams < 1:
		return errors.New("-max-streams must be at least 1")
	case cfg.maxMemoryMiB < 1:
		return errors.New("-max-memory-mib must be at least 1")
	case cfg.maxMessageBytes < minMessageBytes:
		return fmt.Errorf("-max-message-bytes must be at least %d", minMessageBytes)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	builder, err := build.NewBuilder(ctx)
	if err != nil {
		return err
	}
	defer builder.Close(context.Background())
	srv := newServer(cfg, builder)
	defer srv.close()
	if err := srv.selfTest(ctx); err != nil {
		return fmt.Errorf("self-test failed: %w", err)
	}

	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	gs := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.maxMessageBytes), grpc.MaxSendMsgSize(cfg.maxMessageBytes))
	appletpb.RegisterAppletsServer(gs, srv)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(gs, hs)
	reflection.Register(gs)

	serveErr := make(chan error, 1)
	go func() { serveErr <- gs.Serve(lis) }()
	slog.Info("listening", "address", lis.Addr().String())
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	hs.Shutdown()
	stopped := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(shutdownTimeout):
		gs.Stop()
		<-stopped
	}
	return nil
}

// selfTest builds and runs a trivial applet with the server's limits.
func (s *server) selfTest(ctx context.Context) error {
	source := `import type { Applet } from "go-applets";
const applet: Applet<object> = function* (input) { yield* input; };
export default applet;`
	wasm, err := s.builds.build(ctx, source, build.Env{Signature: `import("go-applets").Applet<object>`})
	if err != nil {
		return err
	}
	engine, release, err := s.engines.acquire(ctx, engineKey{memoryMiB: s.cfg.maxMemoryMiB, closeOnContextDone: true})
	if err != nil {
		return err
	}
	defer release()
	applet, err := engine.Load(ctx, wasm)
	if err != nil {
		return err
	}
	defer applet.Close(ctx)
	out, err := applet.RunAll(ctx, nil, []json.RawMessage{json.RawMessage(`{"ok":true}`)}, nil)
	if err != nil {
		return err
	}
	if len(out) != 1 || string(out[0]) != `{"ok":true}` {
		return fmt.Errorf("unexpected output %q", out)
	}
	return nil
}
