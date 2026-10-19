package javy

import (
	"bytes"
	"strings"
	"testing"
)

func TestPlugin(t *testing.T) {
	if !bytes.HasPrefix(Plugin(), []byte("\x00asm")) {
		t.Error("the plugin is not a WebAssembly module")
	}
	if !strings.HasPrefix(Version(), "v") {
		t.Errorf("Version = %q, want a release tag", Version())
	}
	if !strings.Contains(License(), "Apache License") {
		t.Error("License is not Javy's license")
	}
}
