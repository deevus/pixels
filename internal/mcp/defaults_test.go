package mcp

import (
	"strings"
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestDefaultBasesPresent(t *testing.T) {
	for _, name := range []string{"dev", "python", "node"} {
		if _, ok := config.DefaultBases[name]; !ok {
			t.Errorf("DefaultBases missing %q", name)
		}
	}
}

func TestDefaultBaseScriptsLoadable(t *testing.T) {
	for name, b := range config.DefaultBases {
		if !strings.HasPrefix(b.SetupScript, "mcp:") {
			t.Errorf("base %q: SetupScript = %q, want mcp: prefix", name, b.SetupScript)
		}
		path := strings.TrimPrefix(b.SetupScript, "mcp:")
		body, err := DefaultsFS.ReadFile(path)
		if err != nil {
			t.Errorf("base %q: cannot read embedded script %q: %v", name, path, err)
		}
		if len(body) == 0 {
			t.Errorf("base %q: embedded script %q is empty", name, path)
		}
		if !strings.HasPrefix(string(body), "#!") {
			t.Errorf("base %q: script does not start with shebang", name)
		}
	}
}

func TestDefaultBaseChain(t *testing.T) {
	if got := config.DefaultBases["dev"].ParentImage; got == "" {
		t.Error("dev should have parent_image (root of the chain)")
	}
	if got := config.DefaultBases["python"].From; got != "dev" {
		t.Errorf("python.From = %q, want dev", got)
	}
	if got := config.DefaultBases["node"].From; got != "dev" {
		t.Errorf("node.From = %q, want dev", got)
	}
}
