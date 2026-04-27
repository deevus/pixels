package mcp

import (
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestBaseNameAppliesPrefix(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	if got, want := BaseName(cfg, "python"), "px-base-python"; got != want {
		t.Errorf("BaseName = %q, want %q", got, want)
	}
}

func TestBaseNameRespectsCustomPrefix(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "myco-"}}
	if got, want := BaseName(cfg, "python"), "myco-python"; got != want {
		t.Errorf("BaseName = %q, want %q", got, want)
	}
}

func TestBaseNameDefaultsToPxBaseWhenEmpty(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{}}
	if got, want := BaseName(cfg, "python"), "px-base-python"; got != want {
		t.Errorf("BaseName = %q, want %q (empty prefix should fall back to px-base-)", got, want)
	}
}
