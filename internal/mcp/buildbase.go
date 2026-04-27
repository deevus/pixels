package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
)

// InitialCheckpointLabel is the label of the checkpoint created by BuildBase
// after the setup script runs. Sandboxes spawned right after build clone
// from this. Subsequent user `pixels checkpoint create` calls produce
// timestamped labels; the daemon picks by CreatedAt, not label.
const InitialCheckpointLabel = "initial"

// BuildBaseOpts controls BuildBase output and progress reporting.
type BuildBaseOpts struct {
	// Out receives setup-script stdout/stderr. Pass io.Discard or a buffer
	// to silence; nil is treated as io.Discard.
	Out io.Writer
	// Progress is called once before each phase begins. nil = no-op.
	// See BuildBase for the phase strings.
	Progress func(phase string)
}

// BuildBase materialises a base container.
//
// If baseCfg.From is set: clone from the parent base's latest checkpoint into
// <BaseName(cfg, name)>. Otherwise create from baseCfg.ParentImage. Run the
// setup script in the new container, stop it, then create the initial
// checkpoint labelled InitialCheckpointLabel.
//
// opts.Progress fires for these phases (in order):
//   "Cloning from <parent>" or "Creating from <image>"
//   "Waiting for container to be ready..."
//   "Uploading setup script..."
//   "Running setup script..."
//   "Stopping container..."
//   "Creating initial checkpoint..."
func BuildBase(ctx context.Context, be sandbox.Sandbox, cfg *config.Config, name string, baseCfg config.Base, opts BuildBaseOpts) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}

	scriptBytes, err := loadSetupScript(baseCfg.SetupScript)
	if err != nil {
		return fmt.Errorf("base %s: load setup script: %w", name, err)
	}

	target := BaseName(cfg, name)

	// Build the container — either fresh from image or cloned from parent base.
	if baseCfg.From != "" {
		parentName := BaseName(cfg, baseCfg.From)
		progress("Cloning from " + parentName)
		latest, ok, err := LatestCheckpointFor(ctx, be, parentName)
		if err != nil {
			return fmt.Errorf("base %s: lookup parent checkpoint on %s: %w", name, parentName, err)
		}
		if !ok {
			return fmt.Errorf("base %s: parent %s has no checkpoints; build the parent first", name, parentName)
		}
		if err := be.CloneFrom(ctx, parentName, latest.Label, target); err != nil {
			return fmt.Errorf("base %s: clone from %s: %w", name, parentName, err)
		}
	} else if baseCfg.ParentImage != "" {
		progress("Creating from " + baseCfg.ParentImage)
		if _, err := be.Create(ctx, sandbox.CreateOpts{Name: target, Image: baseCfg.ParentImage}); err != nil {
			return fmt.Errorf("base %s: create from image %s: %w", name, baseCfg.ParentImage, err)
		}
	} else {
		return fmt.Errorf("base %s: neither parent_image nor from set", name)
	}

	cleanup := func() {
		if err := be.Delete(context.Background(), target); err != nil {
			fmt.Fprintf(out, "WARN: cleanup of %s after failure: %v\n", target, err)
		}
	}

	progress("Waiting for container to be ready...")
	if err := be.Ready(ctx, target, 5*time.Minute); err != nil {
		cleanup()
		return fmt.Errorf("base %s: ready: %w", name, err)
	}

	// Upload + run setup script.
	progress("Uploading setup script...")
	if err := be.WriteFile(ctx, target, "/tmp/pixels-setup.sh", scriptBytes, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("base %s: upload script: %w", name, err)
	}
	progress("Running setup script...")
	exit, err := be.Run(ctx, target, sandbox.ExecOpts{
		Cmd:    []string{"bash", "/tmp/pixels-setup.sh"},
		Stdout: out,
		Stderr: out,
		Root:   true,
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("base %s: setup script: %w", name, err)
	}
	if exit != 0 {
		cleanup()
		return fmt.Errorf("base %s: setup script exited %d", name, exit)
	}

	// Stop and snapshot.
	progress("Stopping container...")
	if err := be.Stop(ctx, target); err != nil {
		cleanup()
		return fmt.Errorf("base %s: stop: %w", name, err)
	}
	progress("Creating initial checkpoint...")
	if err := be.CreateSnapshot(ctx, target, InitialCheckpointLabel); err != nil {
		cleanup()
		return fmt.Errorf("base %s: snapshot: %w", name, err)
	}
	fmt.Fprintf(out, "==> Base %s ready (initial checkpoint created).\n", name)
	return nil
}

// loadSetupScript reads the setup script from disk or the embedded FS based
// on the path's `mcp:` prefix. Centralised so callers don't open-code.
func loadSetupScript(path string) ([]byte, error) {
	if strings.HasPrefix(path, "mcp:") {
		return DefaultsFS.ReadFile(strings.TrimPrefix(path, "mcp:"))
	}
	return os.ReadFile(path)
}

// LatestCheckpointFor returns the most-recent (by CreatedAt) checkpoint on
// the named container, or ok=false if none exist.
func LatestCheckpointFor(ctx context.Context, be sandbox.Sandbox, container string) (sandbox.Snapshot, bool, error) {
	snaps, err := be.ListSnapshots(ctx, container)
	if err != nil {
		return sandbox.Snapshot{}, false, err
	}
	if len(snaps) == 0 {
		return sandbox.Snapshot{}, false, nil
	}
	latest := snaps[0]
	for _, s := range snaps[1:] {
		if s.CreatedAt.After(latest.CreatedAt) {
			latest = s
		}
	}
	return latest, true, nil
}
