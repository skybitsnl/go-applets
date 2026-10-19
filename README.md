# go-applets

Applets are small, untrusted TypeScript files that change a stream of JSON
objects. They are type-checked, compiled and run safely in a sandboxed
WebAssembly environment, from within a Go application or using a dedicated gRPC
service (planned).

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

**Status:** under development. `build` and `runner` work in their most basic
form: building has no resource limits yet, and runs have no time limits other
than the context the caller passes. The gRPC service does not exist yet.

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

## Development

```sh
go test ./...
go -C javy test ./...
```

The root module uses the `javy` module from this repository, through a
`replace` directive in `go.mod`.

## License

MIT, see [LICENSE](LICENSE), except the `javy` module, which embeds the Javy
plugin and shares Javy's license: the Apache License 2.0 with LLVM
exceptions, see [javy/LICENSE](javy/LICENSE).

`build` embeds the TypeScript compiler through
[go-tsc](https://github.com/skybitsnl/go-tsc), under the Apache License 2.0.
Programs that use `build` must pass on TypeScript's license and notice when they
are distributed; go-tsc provides both.
