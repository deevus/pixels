package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestBuildBaseSequenceOfBackendCalls(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	be := newFakeSandbox()
	var buf bytes.Buffer
	cfg := &config.Config{MCP: config.MCP{BasePrefix: DefaultBasePrefix}}
	err := BuildBase(context.Background(), be, cfg, config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: scriptPath,
	}, "python", &buf)
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if got := len(be.created); got != 1 {
		t.Errorf("expected exactly one builder container created, got %d", got)
	}
	if be.snapshots[BaseName(cfg, "python")] != "ready" {
		t.Errorf("expected snapshot %s; got snapshots=%v", BaseName(cfg, "python"), be.snapshots)
	}
	if got := len(be.stopped); got != 1 || be.stopped[0] != BuilderContainerName("python") {
		t.Errorf("expected builder %s stopped; got stopped=%v", BuilderContainerName("python"), be.stopped)
	}
	// Should have best-effort deleted any existing builder at start.
	if got := len(be.deleted); got != 1 || be.deleted[0] != BuilderContainerName("python") {
		t.Errorf("expected builder %s deleted at start; got deleted=%v", BuilderContainerName("python"), be.deleted)
	}
}
