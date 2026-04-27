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
	err := BuildBase(context.Background(), be, config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: scriptPath,
	}, "python", &buf)
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if got := len(be.created); got != 1 {
		t.Errorf("expected exactly one temp sandbox created, got %d", got)
	}
	if be.snapshots["px-base-python"] != "ready" {
		t.Errorf("expected snapshot px-base-python; got snapshots=%v", be.snapshots)
	}
	if got := len(be.deleted); got != 1 {
		t.Errorf("expected temp deleted; got %v", be.deleted)
	}
}
