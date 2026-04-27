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

// BuildBaseSnapshotName returns the snapshot name for a given base name.
func BuildBaseSnapshotName(name string) string { return "px-base-" + name }

// BuildBase executes one full base-pixel build:
//  1. Create temp sandbox from parent_image
//  2. Wait Ready
//  3. Upload setup_script via Backend.WriteFile
//  4. Run `bash /tmp/pixels-setup.sh` via Backend.Run
//  5. CreateSnapshot named "px-base-<name>"
//  6. Delete the temp sandbox
//
// On any failure it cleans up the temp sandbox and returns the error.
// out receives setup-script stdout/stderr for streaming to the user.
func BuildBase(ctx context.Context, be sandbox.Sandbox, baseCfg config.Base, name string, out io.Writer) error {
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

	tempName := fmt.Sprintf("px-build-%s-%d", name, time.Now().Unix())

	fmt.Fprintf(out, "==> Creating temp sandbox %s from %s\n", tempName, baseCfg.ParentImage)
	if _, err := be.Create(ctx, sandbox.CreateOpts{Name: tempName, Image: baseCfg.ParentImage}); err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	cleanup := func() {
		fmt.Fprintf(out, "==> Cleaning up temp sandbox %s\n", tempName)
		if err := be.Delete(context.Background(), tempName); err != nil {
			fmt.Fprintf(out, "WARN: temp cleanup failed: %v\n", err)
		}
	}

	fmt.Fprintf(out, "==> Waiting for sandbox to be ready\n")
	if err := be.Ready(ctx, tempName, 5*time.Minute); err != nil {
		cleanup()
		return fmt.Errorf("ready: %w", err)
	}

	fmt.Fprintf(out, "==> Uploading setup script (%d bytes)\n", len(scriptBytes))
	if err := be.WriteFile(ctx, tempName, "/tmp/pixels-setup.sh", scriptBytes, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("upload script: %w", err)
	}

	fmt.Fprintf(out, "==> Running setup script\n")
	exit, err := be.Run(ctx, tempName, sandbox.ExecOpts{
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

	fmt.Fprintf(out, "==> Snapshotting as %s\n", BuildBaseSnapshotName(name))
	if err := be.CreateSnapshot(ctx, tempName, BuildBaseSnapshotName(name)); err != nil {
		cleanup()
		return fmt.Errorf("snapshot: %w", err)
	}

	cleanup()
	fmt.Fprintf(out, "==> Done\n")
	return nil
}
