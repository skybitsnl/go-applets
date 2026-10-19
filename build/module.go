package build

import (
	"fmt"
	"strings"

	"github.com/eliben/watgo"
)

// moduleTemplate is the module `javy build -C dynamic` generates, translated
// from Javy's Rust to WebAssembly text format (WAT). See Javy's original source
// for this:
// https://github.com/bytecodealliance/javy/blob/v9.1.0/crates/codegen/src/lib.rs#L283-L370
//
// The module imports the plugin's cabi_realloc, invoke and memory, keeps the
// bytecode in a passive data segment, and exports _start, which allocates
// memory in the plugin, copies the bytecode into it, and invokes it.
//
// The arguments are the plugin's import namespace, the bytecode, and its
// length.
const moduleTemplate = `(module
  (import "%[1]s" "cabi_realloc" (func $cabi_realloc (param i32 i32 i32 i32) (result i32)))
  (import "%[1]s" "invoke" (func $invoke (param i32 i32 i32 i32 i32)))
  (import "%[1]s" "memory" (memory 0))
  (data $bytecode "%[2]s")
  (func (export "_start") (local $ptr i32)
    (memory.init $bytecode
      (local.tee $ptr (call $cabi_realloc (i32.const 0) (i32.const 0) (i32.const 1) (i32.const %[3]d)))
      (i32.const 0) (i32.const %[3]d))
    (call $invoke (local.get $ptr) (i32.const %[3]d) (i32.const 0) (i32.const 0) (i32.const 0))))
`

// module returns a WebAssembly module that runs QuickJS bytecode in the Javy
// plugin with the given import namespace.
func module(namespace string, bytecode []byte) ([]byte, error) {
	wat := fmt.Sprintf(moduleTemplate, watString([]byte(namespace)), watString(bytecode), len(bytecode))
	wasm, err := watgo.CompileWATToWASM([]byte(wat))
	if err != nil {
		return nil, fmt.Errorf("cannot generate module: %w", err)
	}
	return wasm, nil
}

// watString escapes every byte of b for use in a WAT string literal, as \hh.
func watString(b []byte) string {
	const digits = "0123456789abcdef"
	var s strings.Builder
	s.Grow(3 * len(b))
	for _, c := range b {
		s.WriteByte('\\')
		s.WriteByte(digits[c>>4])
		s.WriteByte(digits[c&0xf])
	}
	return s.String()
}
