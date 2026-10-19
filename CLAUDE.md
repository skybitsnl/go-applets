# CLAUDE.md

This repository contains a Go library and gRPC service to run applets. Applets
are small, untrusted TypeScript files that change a stream of JSON objects. They
are type-checked, compiled and run safely in a sandboxed WebAssembly
environment. The user of the library or gRPC service provides their own type
definitions for applet parameters, input objects and output objects.

Applets are expected to be written by an LLM, which should have knowledge of the
build process, runtime environment, our type declarations and the user's type
declarations. The LLM is not in scope of this project, but where possible, this
project should make it convenient.

## How I like to work

- Unless the work is clear-cut and agreed, first present a design and have it
  approved before you start work. Don't start writing before approval.
- Ask before design decisions and changes to the public API. Give a
  recommendation with its reasons, not just a list of options.
- Do not make up work that is not necessary for what was asked. When in doubt,
  ask whether you should include something, or add it into the design as a
  question.
- Never commit, amend, push or tag; I do that myself. Please suggest a commit
  message.
- I often edit code myself between turns. Re-read a file before changing or
  reviewing it, and review it as it is now, not as you remember it.
- Verify claims by running code rather than reasoning about it. Use temporary
  test files, delete them afterwards.
- Before reporting work as done, run `gofmt -l .`, `go vet ./...`,
  `go test ./...` and `go -C javy test ./...`, plus `go test -race` for
  anything concurrent. Say plainly what you did not verify.
- When reviewing, check that tests would fail if the behaviour they cover
  were removed or broken.
- Comments are full sentences, wrapped at 80 columns. Every exported
  identifier has a doc comment. Comments explain why, not what the code
  already says. Prefer self-descriptive code that need no comments.
- The README.md contains code structure; update it if necessary, but keep it
  high-level.

## Goals for the project

- **Generic.** No knowledge of any particular product: no invoices, and no
  references to Centralized or its issue tracker in code, comments or commit
  messages. Examples use neutral types.
- **`runner` knows the protocol, not the language.** It runs any WebAssembly
  module that follows the protocol in its package documentation, and gets
  dependencies such as the Javy plugin as modules from its caller. Nothing in
  `runner` may assume JavaScript or Javy; `build` is one producer of applets.
- **The sandbox is the security boundary.** Untrusted source is only ever
  parsed and run inside WebAssembly. Stopping an applet is preemptive
  (wazero's `CloseOnContextDone`), never dependent on the applet's
  cooperation. The host validates everything that comes from the guest:
  addresses in guest memory, and every output line. Anything that weakens
  this must say so in its documentation, as `WithCloseOnContextDone` does.
- **Mechanism in the library, policy with the caller.** `runner` offers
  contexts and callbacks; limits such as the number or size of outputs are
  the caller's to enforce. Keep the API small.
- **Streaming first.** Nothing may require all input or output in memory at
  once. `RunAll` is a convenience for small runs and tests.
- **Explicit applets.** Everything an applet uses is imported, including
  `Applet` from `"go-applets"`, so applets read clearly to people and to the
  AI models that write them.
- **Deterministic builds.** The same source gives the same module, byte for
  byte, as tests check.
- **Embedded upstream code follows upstream versioning.** The `javy` module's
  version is the version of Javy it embeds, and upstream licenses are passed on.
- **Few dependencies.** This is a security-sensitive library: prefer the
  standard library, and give every dependency a reason.
