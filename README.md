# go-applets

Applets are small, untrusted TypeScript files that change a stream of JSON
objects. They are type-checked, compiled and run safely in a sandboxed
WebAssembly environment, from within a Go application or using a dedicated gRPC
service.

An applet receives its parameters, then a stream of input objects, and produces
a stream of output objects. Applets run with limits on memory and time, and
without access to the network, the filesystem or anything else the host doesn't
explicitly give them.

The repository contains the following components:

- The `javy` directory embeds Javy's plugin, containing a JavaScript on
  WebAssembly compiler and runtime, embedding QuickJS. This is a separate module
  so it can be used without the rest of this repository.
- The `build` directory uses TypeScript (via `skybitsnl/go-tsc`) for
  type-checking applets, `esbuild` for bundling them, Javy for compiling them to
  bytecode, then `watgo` for wrapping the bytecode into a small WASM module.
- The `runner` directory takes a WASM module as produced by `build` (or
  compatible with it), and runs it within a WebAssembly sandbox provided by
  `wazero`.
- The `cmd/appletor` directory contains a command-line tool that runs an applet
  over files, with `tac.ts` as an example.
- The `appletpb` directory has the gRPC API of the service, the Go code
  generated from it, and helpers for Go clients. It is a separate module, so
  that clients do not depend on the rest of this repository.
- The `cmd/appletd` directory contains the gRPC service. It builds and runs
  applets for clients that send their source, declarations and input objects
  over one stream per run. The `Dockerfile` builds its image.
- The `cmd/appletor-remote` directory contains a client of the service: it does
  what `appletor` does, but builds and runs the applet through `appletd`.

**Status:** under development. `build` and `runner` work in their most basic
form: building has no resource limits yet, and runs have no time limits other
than the context the caller passes.

## Usage

The package example of
[`build`](https://pkg.go.dev/github.com/skybitsnl/go-applets/build#example-package)
builds an applet from TypeScript and runs it. The package documentation of
`build` also describes how to have an editor type-check applets.

To try an applet on files, run it with `appletor`. For example, this reverses
the order of the lines in each passed file:

```sh
go run ./cmd/appletor cmd/appletor/tac.ts file-1.txt file-2.txt
```

To run the gRPC service, run `appletd`, or its image. It has no authentication:
run it where only its clients can reach it. `-help` lists its options.

```sh
go run ./cmd/appletd -listen localhost:2080
```

`appletor-remote` runs an applet over files like `appletor`, on that service:

```sh
go run ./cmd/appletor-remote -server localhost:2080 cmd/appletor/tac.ts file-1.txt file-2.txt
```

## Development

```sh
go test ./...
go -C javy test ./...
```

The root module uses the `javy` and `appletpb` modules from this repository,
through `replace` directives in `go.mod`.

## License

MIT, see [LICENSE](LICENSE), except the `javy` module, which embeds the Javy
plugin and shares Javy's license: the Apache License 2.0 with LLVM
exceptions, see [javy/LICENSE](javy/LICENSE).

`build` embeds the TypeScript compiler through
[go-tsc](https://github.com/skybitsnl/go-tsc), under the Apache License 2.0.
Programs that use `build` must pass on TypeScript's license and notice when they
are distributed; go-tsc provides both.
