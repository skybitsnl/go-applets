// Package javy embeds the default plugin of Javy
// (https://github.com/bytecodealliance/javy), unmodified, for use with a
// WebAssembly runtime such as wazero. update.sh downloads a new release.
//
// The version of this module is the version of Javy it embeds: javy/v9.1.0
// contains Javy v9.1.0.
//
// Like Javy, this package and the plugin are licensed under the Apache
// License 2.0 with LLVM exceptions, available as License, which must be
// passed on when distributing the plugin.
package javy

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"strings"
)

//go:embed plugin/plugin.wasm.gz
var pluginGz []byte

//go:embed plugin/VERSION
var version string

//go:embed LICENSE
var license string

// License is the license of this package and the plugin: Javy's license.
func License() string {
	return license
}

// Version is the Javy release the plugin comes from, e.g. "v9.1.0".
func Version() string {
	return strings.TrimSpace(version)
}

// Plugin returns the plugin's WebAssembly module. Each call decompresses it
// anew, so callers should compile it once and reuse the result.
func Plugin() []byte {
	r, err := gzip.NewReader(bytes.NewReader(pluginGz))
	if err != nil {
		panic("javy: embedded plugin is corrupt: " + err.Error())
	}
	wasm, err := io.ReadAll(r)
	if err != nil {
		panic("javy: embedded plugin is corrupt: " + err.Error())
	}
	return wasm
}
