// Package build turns the TypeScript source of an applet into a WebAssembly
// module that package runner can run.
//
// Building has three steps, which can also be done separately:
//
//   - Check type-checks the source with tsc, against declarations of the
//     caller.
//   - Bundle strips the types and bundles the source with a prelude, which
//     implements the protocol of package runner, into a single script. The
//     applet cannot import anything.
//   - Compile compiles the script to QuickJS bytecode with the Javy plugin, and
//     wraps it in a small module that runs it. That module imports its
//     JavaScript engine from the Javy plugin; see Builder.Dependencies.
//
// # Type checking in an editor
//
// To have an editor type-check applets as Check does, copy runtime.d.ts and
// tsconfig.json from this package's directory into the directory where you
// keep your applets, together with your declarations (Env.Declarations) as a
// .d.ts file. That tsconfig.json lists no files, so the editor checks every
// TypeScript file in the directory with them. Copy both files again when you
// upgrade this module, as they may change.
//
// The editor does not check the default export against Env.Signature. Giving
// the export that type has the same effect:
//
//	import type { Applet } from "go-applets";
//	import type { Order } from "shop";
//
//	const applet: Applet<Order> = function* (input) { ... };
//	export default applet;
package build

import (
	"context"
	"errors"

	"github.com/tetratelabs/wazero"
)

// Builder builds applets. It is safe for concurrent use.
type Builder struct {
	typechecker typechecker

	// Compilation members
	rt        wazero.Runtime
	plugin    wazero.CompiledModule
	namespace string
}

// NewBuilder creates a new empty builder. When used, it may create temporary
// directories. Ensure that `Close()` is called when you're done with a Builder
// so they don't leak.
func NewBuilder(ctx context.Context) (*Builder, error) {
	b := &Builder{}

	if err := b.initCompiler(ctx); err != nil {
		b.Close(ctx)
		return nil, err
	}

	return b, nil
}

// Close releases the builder, and removes the temporary directory with tsc if
// Check created one. It must not be called while the builder is in use.
func (b *Builder) Close(ctx context.Context) error {
	var err error
	if b.rt != nil {
		err = b.rt.Close(ctx)
	}
	return errors.Join(err, b.typechecker.close())
}

// Build typechecks, bundles and compiles an applet. If the source is invalid,
// it returns the InvalidSourceError of the step that found out.
//
// The returned module is small: it contains the bytecode, and imports the
// JavaScript engine from the modules returned by Dependencies. To run it,
// create a runner.Engine with those dependencies, and load the module into it:
//
//	engine, err := runner.NewEngine(ctx, b.Dependencies(), runner.Limits{})
//	...
//	applet, err := engine.Load(ctx, wasm)
//
// The bytecode only works with the plugin that compiled it. A module kept for
// later must therefore run with the Dependencies of the same version of this
// package.
func (b *Builder) Build(ctx context.Context, source string, env Env) ([]byte, error) {
	if err := b.Check(ctx, source, env); err != nil {
		return nil, err
	}
	js, err := Bundle(source)
	if err != nil {
		return nil, err
	}
	return b.Compile(ctx, js)
}
