package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
)

// BuildBase executes one full base-pixel build:
//  1. Delete any existing builder container for this base (clean slate)
//  2. Create new builder container from parent_image
//  3. Wait Ready
//  4. Upload setup_script via Backend.WriteFile
//  5. Run `bash /tmp/pixels-setup.sh` via Backend.Run
//  6. CreateSnapshot
//  7. Stop the builder container (keeps snapshot alive for CloneFrom)
//
// On any failure it cleans up and returns the error.
// out receives setup-script stdout/stderr for streaming to the user.
func BuildBase(ctx context.Context, be sandbox.Sandbox, cfg *config.Config, baseCfg config.Base, name string, out io.Writer) error {
	if baseCfg.ParentImage == "" {
		return fmt.Errorf("base %q: parent_image not set", name)
	}
	if baseCfg.SetupScript == "" {
		return fmt.Errorf("base %q: setup_script not set", name)
	}

	scriptBytes, err := os.ReadFile(baseCfg.SetupScript)
	if err != nil {
		return fmt.Errorf("read setup_script %s: %w", baseCfg.SetupScript, err)
	}

	builderName := BuilderContainerName(name)
	snapName := BaseName(cfg, name)
	cleanup := func() {
		fmt.Fprintf(out, "==> Cleaning up builder %s\n", builderName)
		if err := be.Delete(context.Background(), builderName); err != nil {
			fmt.Fprintf(out, "WARN: cleanup failed: %v\n", err)
		}
	}

	// Delete any existing builder container for a clean build.
	fmt.Fprintf(out, "==> Removing existing builder (if any)\n")
	_ = be.Delete(ctx, builderName) // best-effort; may not exist

	fmt.Fprintf(out, "==> Creating builder %s from %s\n", builderName, baseCfg.ParentImage)
	if _, err := be.Create(ctx, sandbox.CreateOpts{Name: builderName, Image: baseCfg.ParentImage}); err != nil {
		return fmt.Errorf("create builder: %w", err)
	}

	fmt.Fprintf(out, "==> Waiting for sandbox to be ready\n")
	if err := be.Ready(ctx, builderName, 5*time.Minute); err != nil {
		cleanup()
		return fmt.Errorf("ready: %w", err)
	}

	fmt.Fprintf(out, "==> Uploading setup script (%d bytes)\n", len(scriptBytes))
	if err := be.WriteFile(ctx, builderName, "/tmp/pixels-setup.sh", scriptBytes, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("upload script: %w", err)
	}

	fmt.Fprintf(out, "==> Running setup script\n")
	exit, err := be.Run(ctx, builderName, sandbox.ExecOpts{
		Cmd:    []string{"bash", "/tmp/pixels-setup.sh"},
		Stdout: out,
		Stderr: out,
		Root:   true,
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("setup script: %w", err)
	}
	if exit != 0 {
		cleanup()
		return fmt.Errorf("setup script exited %d", exit)
	}

	fmt.Fprintf(out, "==> Snapshotting as %s\n", snapName)
	if err := be.CreateSnapshot(ctx, builderName, snapName); err != nil {
		cleanup()
		return fmt.Errorf("snapshot: %w", err)
	}

	// Stop (don't delete) — the snapshot lives on this container.
	if err := be.Stop(ctx, builderName); err != nil {
		cleanup()
		return fmt.Errorf("stop builder: %w", err)
	}
	fmt.Fprintf(out, "==> Done (builder %s stopped, snapshot %s ready)\n", builderName, snapName)
	return nil
}
