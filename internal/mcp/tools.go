package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
)

// Tools is the dependency bundle every MCP handler closes over.
type Tools struct {
	State          *State
	Backend        sandbox.Sandbox
	Prefix         string
	DefaultImage   string
	ExecTimeoutMax time.Duration
	Log            *slog.Logger  // not nil; defaults to NopLogger if not set
	Locks          *SandboxLocks // shared with Reaper; never nil after NewServer
	DaemonCtx      context.Context // outlives any single request; provisioning goroutine inherits this
	Cfg            *config.Config
	Builder         *Builder
	BuildLockDir    string
	provisionWG     sync.WaitGroup // test affordance: tracks in-flight provisioning goroutines
}

// WaitProvisioning blocks until all in-flight provisioning goroutines complete.
// Used by the daemon shutdown path so final State writes (MarkRunning /
// MarkFailed / SetIP) make it to disk before the process exits.
func (t *Tools) WaitProvisioning() { t.provisionWG.Wait() }

func (t *Tools) log() *slog.Logger {
	if t.Log == nil {
		return NopLogger()
	}
	return t.Log
}

// persist saves state and logs save errors. Returns the error so callers can
// decide whether to surface it.
func (t *Tools) persist() error {
	if err := t.State.Save(); err != nil {
		t.log().Error("state save failed", "err", err)
		return err
	}
	return nil
}



// --- Input/output types ---

type CreateSandboxIn struct {
	Label string `json:"label,omitempty"`
	Image string `json:"image,omitempty"`
	Base  string `json:"base,omitempty"`
}
type CreateSandboxOut struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	Status string `json:"status"`
}

type SandboxRef struct {
	Name string `json:"name"`
}
type Ack struct {
	OK bool `json:"ok"`
}

type ListSandboxesOut struct {
	Sandboxes []SandboxView `json:"sandboxes"`
}
type SandboxView struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	IP             string    `json:"ip,omitempty"`
	Base           string    `json:"base,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	IdleFor        string    `json:"idle_for"`
}

type ExecIn struct {
	Name       string            `json:"name"`
	Command    []string          `json:"command"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
}
type ExecOut struct {
	ExitCode       int    `json:"exit_code"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	TransportError string `json:"transport_error,omitempty"` // non-empty when the underlying SSH/exec channel failed
}

type WriteFileIn struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"` // octal string e.g. "0644"
}
type WriteFileOut struct {
	OK           bool `json:"ok"`
	BytesWritten int  `json:"bytes_written"`
}

type ReadFileIn struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}
type ReadFileOut struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type ListFilesIn struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}
type ListFilesOut struct {
	Entries []sandbox.FileEntry `json:"entries"`
}

type EditFileIn struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}
type EditFileOut struct {
	OK           bool `json:"ok"`
	Replacements int  `json:"replacements"`
}

type DeleteFileIn struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// --- Helpers ---

func (t *Tools) generateName() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return t.Prefix + hex.EncodeToString(b[:])
}

func (t *Tools) requireSandbox(name string) (Sandbox, error) {
	sb, ok := t.State.Get(name)
	if !ok {
		return sb, fmt.Errorf("sandbox %q not found", name)
	}
	return sb, nil
}

func parseMode(s string, fallback os.FileMode) os.FileMode {
	if s == "" {
		return fallback
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return fallback
	}
	return os.FileMode(n)
}

// --- Lifecycle handlers ---

func (t *Tools) CreateSandbox(ctx context.Context, in CreateSandboxIn) (CreateSandboxOut, error) {
	image := in.Image
	if image == "" {
		image = t.DefaultImage
	}
	name := t.generateName()
	now := time.Now().UTC()

	t.State.Add(Sandbox{
		Name:           name,
		Label:          in.Label,
		Image:          image,
		Base:           in.Base,
		Status:         "provisioning",
		CreatedAt:      now,
		LastActivityAt: now,
	})
	if err := t.persist(); err != nil {
		t.State.Remove(name)
		return CreateSandboxOut{}, fmt.Errorf("create %s: state save failed: %w", name, err)
	}

	t.provisionWG.Add(1)
	go func() {
		defer t.provisionWG.Done()
		t.provision(name, in)
	}()

	return CreateSandboxOut{Name: name, Status: "provisioning"}, nil
}

func (t *Tools) provision(name string, in CreateSandboxIn) {
	m := t.Locks.For(name)
	m.Lock()
	defer m.Unlock()

	// Re-check: a destroy_sandbox call may have run between request return
	// and lock acquisition. If the sandbox is gone from state, exit cleanly.
	if _, ok := t.State.Get(name); !ok {
		t.log().Debug("provisioning aborted; sandbox already removed", "name", name)
		return
	}

	ctx := t.DaemonCtx
	if ctx == nil {
		ctx = context.Background()
	}

	if in.Base != "" {
		t.provisionFromBase(ctx, name, in)
		return
	}
	t.provisionFromImage(ctx, name, in)
}

func (t *Tools) provisionFromImage(ctx context.Context, name string, in CreateSandboxIn) {
	image := in.Image
	if image == "" {
		image = t.DefaultImage
	}

	inst, err := t.Backend.Create(ctx, sandbox.CreateOpts{Name: name, Image: image})
	if err != nil {
		t.log().Error("create failed", "name", name, "err", err)
		t.State.MarkFailed(name, err)
		_ = t.persist()
		return
	}

	if err := t.Backend.Ready(ctx, name, 2*time.Minute); err != nil {
		t.log().Error("ready timed out", "name", name, "err", err)
		t.State.MarkFailed(name, err)
		_ = t.persist()
		return
	}

	if len(inst.Addresses) > 0 {
		t.State.SetIP(name, inst.Addresses[0])
	}
	t.State.MarkRunning(name)
	t.State.BumpActivity(name, time.Now().UTC())
	if ctx.Err() == nil {
		_ = t.persist()
	}
	t.log().Info("provisioning complete", "name", name)
}

func (t *Tools) provisionFromBase(ctx context.Context, name string, in CreateSandboxIn) {
	if t.Cfg == nil {
		t.State.MarkFailed(name, fmt.Errorf("config not loaded; base provisioning unavailable"))
		_ = t.persist()
		return
	}
	if _, ok := t.Cfg.MCP.Bases[in.Base]; !ok {
		t.State.MarkFailed(name, fmt.Errorf("base %q not declared in config", in.Base))
		_ = t.persist()
		return
	}

	// Ensure the snapshot exists (build if not). This blocks for the duration
	// of the build, which may be minutes — that's fine; we're in a goroutine.
	builder := BuilderContainerName(in.Base)
	snapLabel := SnapshotName(in.Base)
	exists, err := t.Backend.SnapshotExists(ctx, builder, snapLabel)
	if err != nil {
		t.log().Warn("snapshot existence check failed; will rebuild", "base", in.Base, "err", err)
		exists = false
	}
	if !exists {
		if err := t.Builder.Build(ctx, in.Base); err != nil {
			t.State.MarkFailed(name, fmt.Errorf("build base %s: %w", in.Base, err))
			_ = t.persist()
			return
		}
		// Re-check state: a destroy may have happened during the build.
		if _, ok := t.State.Get(name); !ok {
			return
		}
	}

	if err := t.Backend.CloneFrom(ctx, builder, snapLabel, name); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("clone: %w", err))
		_ = t.persist()
		return
	}

	if err := t.Backend.Ready(ctx, name, 2*time.Minute); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("ready: %w", err))
		_ = t.persist()
		return
	}

	if inst, err := t.Backend.Get(ctx, name); err == nil && len(inst.Addresses) > 0 {
		t.State.SetIP(name, inst.Addresses[0])
	}
	t.State.MarkRunning(name)
	t.State.BumpActivity(name, time.Now().UTC())
	if ctx.Err() == nil {
		_ = t.persist()
	}
	t.log().Info("cloned from base", "name", name, "base", in.Base)
}

// BuilderContainerName returns the fixed container name that holds the
// snapshot for the given base. This is the name of the stopped builder
// container that BuildBase creates and keeps alive.
func BuilderContainerName(baseName string) string { return "px-base-builder-" + baseName }

// SnapshotName returns the snapshot label for a base, created by BuildBase
// on the builder container.
func SnapshotName(baseName string) string { return "px-base-" + baseName }

func (t *Tools) DestroySandbox(ctx context.Context, in SandboxRef) (Ack, error) {
	if err := t.Backend.Delete(ctx, in.Name); err != nil {
		return Ack{}, err
	}
	t.State.Remove(in.Name)
	_ = t.persist()
	return Ack{OK: true}, nil
}

func (t *Tools) StopSandbox(ctx context.Context, in SandboxRef) (Ack, error) {
	if err := t.Backend.Stop(ctx, in.Name); err != nil {
		return Ack{}, err
	}
	t.State.SetStatus(in.Name, "stopped")
	_ = t.persist()
	return Ack{OK: true}, nil
}

func (t *Tools) StartSandbox(ctx context.Context, in SandboxRef) (CreateSandboxOut, error) {
	if err := t.Backend.Start(ctx, in.Name); err != nil {
		return CreateSandboxOut{}, err
	}
	inst, err := t.Backend.Get(ctx, in.Name)
	if err != nil {
		return CreateSandboxOut{}, err
	}
	ip := ""
	if len(inst.Addresses) > 0 {
		ip = inst.Addresses[0]
	}
	t.State.SetStatus(in.Name, "running")
	t.State.BumpActivity(in.Name, time.Now().UTC())
	if err := t.persist(); err != nil {
		return CreateSandboxOut{}, fmt.Errorf("start %s: state save failed: %w", in.Name, err)
	}
	return CreateSandboxOut{Name: in.Name, IP: ip, Status: "running"}, nil
}

// EmptyIn is a placeholder input type for tools that take no arguments.
type EmptyIn struct{}

func (t *Tools) ListSandboxes(ctx context.Context, _ EmptyIn) (ListSandboxesOut, error) {
	now := time.Now().UTC()
	in := t.State.Sandboxes()
	out := make([]SandboxView, 0, len(in))
	for _, sb := range in {
		out = append(out, SandboxView{
			Name:           sb.Name,
			Label:          sb.Label,
			Status:         sb.Status,
			Error:          sb.Error,
			IP:             sb.IP,
			Base:           sb.Base,
			CreatedAt:      sb.CreatedAt,
			LastActivityAt: sb.LastActivityAt,
			IdleFor:        now.Sub(sb.LastActivityAt).Round(time.Second).String(),
		})
	}
	return ListSandboxesOut{Sandboxes: out}, nil
}

// --- ListBases handler ---

type ListBasesOut struct {
	Bases []BaseView `json:"bases"`
}

type BaseView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ParentImage string `json:"parent_image"`
	Status      string `json:"status"` // "ready" | "missing" | "building" | "failed"
	Error       string `json:"error,omitempty"`
}

func (t *Tools) ListBases(ctx context.Context, _ EmptyIn) (ListBasesOut, error) {
	if t.Cfg == nil {
		return ListBasesOut{}, nil
	}
	out := make([]BaseView, 0, len(t.Cfg.MCP.Bases))
	for name, b := range t.Cfg.MCP.Bases {
		view := BaseView{
			Name:        name,
			Description: b.Description,
			ParentImage: b.ParentImage,
		}

		// In-flight or recently failed?
		if t.Builder != nil {
			if status, err := t.Builder.Status(name); status != "" {
				view.Status = status
				if err != nil {
					view.Error = err.Error()
				}
				out = append(out, view)
				continue
			}
		}

		// Snapshot present or absent?
		exists, err := t.Backend.SnapshotExists(ctx, BuilderContainerName(name), SnapshotName(name))
		if err != nil {
			t.log().Warn("snapshot existence check failed", "base", name, "err", err)
			exists = false
		}
		if exists {
			view.Status = "ready"
		} else {
			view.Status = "missing"
		}
		out = append(out, view)
	}
	return ListBasesOut{Bases: out}, nil
}

// --- Exec handler ---

func (t *Tools) Exec(ctx context.Context, in ExecIn) (ExecOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ExecOut{}, err
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 || timeout > t.ExecTimeoutMax {
		timeout = t.ExecTimeoutMax
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mu := t.Locks.For(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	// Wrap the command in `env [-C cwd] [KEY=val ...] -- <command>` so cwd and
	// env take effect regardless of how the backend handles ExecOpts.Env.
	prefix := []string{"env"}
	if in.Cwd != "" {
		prefix = append(prefix, "-C", in.Cwd)
	}
	keys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		prefix = append(prefix, k+"="+in.Env[k])
	}
	prefix = append(prefix, "--")
	cmd := append(prefix, in.Command...)

	var stdout, stderr strings.Builder
	exit, err := t.Backend.Run(ctx, sb.Name, sandbox.ExecOpts{
		Cmd:    cmd,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.persist()

	out := ExecOut{
		ExitCode: exit,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
	if err != nil {
		out.TransportError = err.Error()
	}
	return out, nil
}

// --- File handlers ---

func (t *Tools) WriteFile(ctx context.Context, in WriteFileIn) (WriteFileOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return WriteFileOut{}, err
	}
	mode := parseMode(in.Mode, 0o644)
	mu := t.Locks.For(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	if err := t.Backend.WriteFile(ctx, sb.Name, in.Path, []byte(in.Content), mode); err != nil {
		return WriteFileOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.persist()
	return WriteFileOut{OK: true, BytesWritten: len(in.Content)}, nil
}

func (t *Tools) ReadFile(ctx context.Context, in ReadFileIn) (ReadFileOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ReadFileOut{}, err
	}
	mu := t.Locks.For(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	body, truncated, err := t.Backend.ReadFile(ctx, sb.Name, in.Path, in.MaxBytes)
	if err != nil {
		return ReadFileOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.persist()
	return ReadFileOut{Content: string(body), Truncated: truncated}, nil
}

func (t *Tools) ListFiles(ctx context.Context, in ListFilesIn) (ListFilesOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ListFilesOut{}, err
	}
	mu := t.Locks.For(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	entries, err := t.Backend.ListFiles(ctx, sb.Name, in.Path, in.Recursive)
	if err != nil {
		return ListFilesOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.persist()
	return ListFilesOut{Entries: entries}, nil
}

// EditFile is read-modify-write composed in the MCP layer. It mirrors the
// shape of Claude's Edit tool: errors if old_string is missing or non-unique
// (unless replace_all is true).
func (t *Tools) EditFile(ctx context.Context, in EditFileIn) (EditFileOut, error) {
	if in.OldString == "" {
		return EditFileOut{}, fmt.Errorf("old_string must not be empty")
	}
	if in.OldString == in.NewString {
		return EditFileOut{}, fmt.Errorf("old_string and new_string are identical")
	}
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return EditFileOut{}, err
	}
	mu := t.Locks.For(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	body, _, err := t.Backend.ReadFile(ctx, sb.Name, in.Path, 0)
	if err != nil {
		return EditFileOut{}, fmt.Errorf("read: %w", err)
	}
	original := string(body)
	count := strings.Count(original, in.OldString)
	switch {
	case count == 0:
		return EditFileOut{}, fmt.Errorf("old_string not found in %s", in.Path)
	case count > 1 && !in.ReplaceAll:
		return EditFileOut{}, fmt.Errorf("old_string appears %d times in %s; pass replace_all=true or include more context", count, in.Path)
	}

	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(original, in.OldString, in.NewString)
	} else {
		updated = strings.Replace(original, in.OldString, in.NewString, 1)
	}

	if err := t.Backend.WriteFile(ctx, sb.Name, in.Path, []byte(updated), 0o644); err != nil {
		return EditFileOut{}, fmt.Errorf("write: %w", err)
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.persist()
	return EditFileOut{OK: true, Replacements: count}, nil
}

func (t *Tools) DeleteFile(ctx context.Context, in DeleteFileIn) (Ack, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return Ack{}, err
	}
	mu := t.Locks.For(sb.Name)
	mu.Lock()
	defer mu.Unlock()
	if err := t.Backend.DeleteFile(ctx, sb.Name, in.Path); err != nil {
		return Ack{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.persist()
	return Ack{OK: true}, nil
}
