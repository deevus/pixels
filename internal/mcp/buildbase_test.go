package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
)

func TestBuildBaseFromParentImage(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, cfg, "python", config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: scriptPath,
	}, BuildBaseOpts{Out: &buf})
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if len(be.created) != 1 || be.created[0].Image != "images:ubuntu/24.04" {
		t.Errorf("expected one container created from parent_image; got %v", be.created)
	}
	if be.created[0].Name != "px-base-python" {
		t.Errorf("container name = %q, want px-base-python", be.created[0].Name)
	}
	got, ok := be.snapshots["px-base-python:initial"]
	if !ok {
		t.Errorf("expected initial checkpoint on px-base-python; key missing from snapshots=%v", be.snapshots)
	}
	if ts, isTime := got.(time.Time); !isTime || ts.IsZero() {
		t.Errorf("expected non-zero time.Time at px-base-python:initial; got %#v", got)
	}
}

func TestBuildBaseFromParentBase(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "setup.sh")
	_ = os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi\n"), 0o755)

	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}

	// Pretend the parent base "dev" already exists with one checkpoint.
	be.containers["px-base-dev"] = sandbox.Instance{Name: "px-base-dev", Status: sandbox.StatusStopped}
	be.snapshots["px-base-dev:initial"] = time.Now()

	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, cfg, "python", config.Base{
		From:        "dev",
		SetupScript: scriptPath,
	}, BuildBaseOpts{Out: &buf})
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if len(be.clonedNew) != 1 {
		t.Fatalf("expected one clone-from operation; got %v", be.clonedNew)
	}
	if be.clonedNew[0].source != "px-base-dev" {
		t.Errorf("clone source = %q, want px-base-dev", be.clonedNew[0].source)
	}
	if be.clonedNew[0].dest != "px-base-python" {
		t.Errorf("clone dest = %q, want px-base-python", be.clonedNew[0].dest)
	}
}

func TestBuildBaseEmbeddedSetupScript(t *testing.T) {
	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, cfg, "dev", config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: "mcp:bases/dev.sh",
	}, BuildBaseOpts{Out: &buf})
	if err != nil {
		t.Fatalf("BuildBase with embedded script: %v", err)
	}
	// Confirm the embedded script ran (fakeSandbox.Run records the cmd)
	var sawSetup bool
	for _, c := range be.runs {
		if strings.Contains(strings.Join(c, " "), "bash") {
			sawSetup = true
		}
	}
	if !sawSetup {
		t.Error("expected a bash setup-script run on the new base")
	}
}

func TestBuildBaseProgressPhases(t *testing.T) {
	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	be.containers["px-base-dev"] = sandbox.Instance{Name: "px-base-dev", Status: sandbox.StatusStopped}
	be.snapshots["px-base-dev:initial"] = time.Now()

	var phases []string
	err := BuildBase(context.Background(), be, cfg, "python", config.Base{
		From:        "dev",
		SetupScript: "mcp:bases/python.sh",
	}, BuildBaseOpts{Progress: func(p string) { phases = append(phases, p) }})
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	want := []string{
		"Cloning from px-base-dev",
		"Waiting for container to be ready...",
		"Uploading setup script...",
		"Running setup script...",
		"Stopping container...",
		"Creating initial checkpoint...",
	}
	if len(phases) != len(want) {
		t.Fatalf("got %d phases, want %d: %v", len(phases), len(want), phases)
	}
	for i, w := range want {
		if phases[i] != w {
			t.Errorf("phase[%d] = %q, want %q", i, phases[i], w)
		}
	}
}

func TestBuildBaseProgressFromImage(t *testing.T) {
	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	var phases []string
	err := BuildBase(context.Background(), be, cfg, "dev", config.Base{
		ParentImage: "ubuntu/24.04",
		SetupScript: "mcp:bases/dev.sh",
	}, BuildBaseOpts{Progress: func(p string) { phases = append(phases, p) }})
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}
	if len(phases) == 0 || phases[0] != "Creating from ubuntu/24.04" {
		t.Errorf("first phase = %q, want %q (got phases=%v)", phases[0], "Creating from ubuntu/24.04", phases)
	}
}
