package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
	"github.com/deevus/pixels/sandbox/user"
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

	// reconcileTTL controls how often ListSandboxes reconciles in-memory state
	// against backend reality. Zero defaults to reconcileDefaultTTL.
	reconcileTTL time.Duration
	reconcileMu  sync.Mutex
	lastSync     time.Time
}

// reconcileDefaultTTL is how often ListSandboxes will query the backend to
// reconcile state. Bursty callers within the window read cached state; after
// the window, the next caller pays one round-trip.
const reconcileDefaultTTL = 15 * time.Second

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

// touch bumps the sandbox's last-activity timestamp and persists state,
// swallowing any save error (already logged by persist).
func (t *Tools) touch(name string) {
	t.State.BumpActivity(name, time.Now().UTC())
	_ = t.persist()
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

// editFileMaxBytes caps the in-memory read for EditFile. Editing past this
// would silently truncate the rest of the file on write-back.
const editFileMaxBytes = 10 * 1024 * 1024

// readFileDefaultMaxBytes is the implicit cap when a caller omits MaxBytes.
// readFileHardMaxBytes is the absolute upper bound; larger requests are clamped.
const (
	readFileDefaultMaxBytes int64 = 1 * 1024 * 1024
	readFileHardMaxBytes    int64 = 10 * 1024 * 1024
)

func parseMode(s string, fallback os.FileMode) (os.FileMode, error) {
	if s == "" {
		return fallback, nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid mode %q: must be octal (e.g. \"0644\", \"755\"): %w", s, err)
	}
	return os.FileMode(n), nil
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

	if _, err := t.Backend.Create(ctx, sandbox.CreateOpts{Name: name, Image: image}); err != nil {
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

	t.finalizeProvisioning(ctx, name)
	t.log().Info("provisioning complete", "name", name)
}

// finalizeProvisioning records a successful provision: best-effort IP capture,
// running state, activity bump, and persist (skipped if ctx was cancelled).
func (t *Tools) finalizeProvisioning(ctx context.Context, name string) {
	if inst, err := t.Backend.Get(ctx, name); err == nil && len(inst.Addresses) > 0 {
		t.State.SetIP(name, inst.Addresses[0])
	}
	t.State.MarkRunning(name)
	t.State.BumpActivity(name, time.Now().UTC())
	if ctx.Err() == nil {
		_ = t.persist()
	}
}

func (t *Tools) provisionFromBase(ctx context.Context, name string, in CreateSandboxIn) {
	// BuildChain validates the base is declared.
	// Cascade build any missing links in the from-chain.
	exists := func(container string) bool {
		_, err := t.Backend.Get(ctx, container)
		return err == nil
	}
	build := func(baseName string) error {
		return t.Builder.Build(ctx, baseName)
	}
	if err := BuildChain(ctx, t.Cfg, in.Base, exists, build); err != nil {
		t.State.MarkFailed(name, err)
		_ = t.persist()
		return
	}

	// All chain links are present. Look up the latest checkpoint on the target base.
	target := BaseName(t.Cfg, in.Base)
	latest, ok2, err := LatestCheckpointFor(ctx, t.Backend, target)
	if err != nil {
		t.State.MarkFailed(name, fmt.Errorf("list checkpoints on %s: %w", target, err))
		_ = t.persist()
		return
	}
	if !ok2 {
		t.State.MarkFailed(name, fmt.Errorf("base %s has no checkpoints; run `pixels checkpoint create %s`", in.Base, target))
		_ = t.persist()
		return
	}

	// Clone the sandbox.
	if err := t.Backend.CloneFrom(ctx, target, latest.Label, name); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("clone: %w", err))
		_ = t.persist()
		return
	}
	if err := t.Backend.Ready(ctx, name, 2*time.Minute); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("ready: %w", err))
		_ = t.persist()
		return
	}

	t.finalizeProvisioning(ctx, name)
}


func (t *Tools) DestroySandbox(ctx context.Context, in SandboxRef) (Ack, error) {
	if err := t.Backend.Delete(ctx, in.Name); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return Ack{}, err
	}
	// Backend either deleted the instance or it was already gone. Either way,
	// drop the state record so ghosts don't accumulate.
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
	t.reconcileWithBackend(ctx)
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

// reconcileWithBackend updates in-memory state from backend reality, no-op if
// the last reconcile is fresher than reconcileTTL. Failures from individual
// Backend.Get calls are silently ignored — reconciliation is best-effort and
// must not interfere with returning the cached snapshot.
//
// Transitions handled:
//   - "failed" sandbox whose backend container is RUNNING with an IP → flip
//     to "running", clear the error. Recovers the slow-DHCP race where Ready
//     gave up before the IP appeared.
//   - "running" sandbox whose backend container is STOPPED → flip to "stopped".
//   - "stopped" sandbox whose backend container is RUNNING → flip to "running".
//   - State record whose backend container is gone (ErrNotFound) → leave alone;
//     the next destroy_sandbox call will clean up the ghost.
func (t *Tools) reconcileWithBackend(ctx context.Context) {
	ttl := t.reconcileTTL
	if ttl == 0 {
		ttl = reconcileDefaultTTL
	}
	t.reconcileMu.Lock()
	if time.Since(t.lastSync) < ttl {
		t.reconcileMu.Unlock()
		return
	}
	t.lastSync = time.Now()
	t.reconcileMu.Unlock()

	dirty := false
	for _, sb := range t.State.Sandboxes() {
		inst, err := t.Backend.Get(ctx, sb.Name)
		if err != nil {
			// Missing or transient: leave state alone.
			continue
		}
		ip := ""
		if len(inst.Addresses) > 0 {
			ip = inst.Addresses[0]
		}

		switch {
		case inst.Status == sandbox.StatusRunning && ip != "":
			if sb.Status != "running" || sb.IP != ip {
				t.State.MarkRunning(sb.Name)
				t.State.SetIP(sb.Name, ip)
				dirty = true
			}
		case inst.Status == sandbox.StatusStopped:
			if sb.Status != "stopped" {
				t.State.SetStatus(sb.Name, "stopped")
				dirty = true
			}
		}
	}
	if dirty {
		_ = t.persist()
	}
}

// --- ListBases handler ---

type ListBasesOut struct {
	Bases []BaseView `json:"bases"`
}

type BaseView struct {
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	ParentImage    string     `json:"parent_image,omitempty"`
	From           string     `json:"from,omitempty"`
	Status         string     `json:"status"` // "ready" | "missing" | "building" | "failed"
	Error          string     `json:"error,omitempty"`
	LastCheckpoint *time.Time `json:"last_checkpoint,omitempty"`
}

func (t *Tools) ListBases(ctx context.Context, _ EmptyIn) (ListBasesOut, error) {
	if t.Cfg == nil {
		return ListBasesOut{}, nil
	}
	out := make([]BaseView, 0, len(t.Cfg.MCP.Bases))
	for name, b := range t.Cfg.MCP.Bases {
		v := BaseView{
			Name:        name,
			Description: b.Description,
			ParentImage: b.ParentImage,
			From:        b.From,
		}

		// In-flight or recently failed?
		if t.Builder != nil {
			if status, err := t.Builder.Status(name); status != "" {
				v.Status = status
				if err != nil {
					v.Error = err.Error()
				}
				out = append(out, v)
				continue
			}
		}

		// Container existence + checkpoint timestamp.
		container := BaseName(t.Cfg, name)
		if _, err := t.Backend.Get(ctx, container); err != nil {
			if errors.Is(err, sandbox.ErrNotFound) {
				v.Status = "missing"
				out = append(out, v)
				continue
			}
			v.Status = "failed"
			v.Error = err.Error()
			out = append(out, v)
			continue
		}
		// Container exists. Determine readiness from checkpoint presence:
		// no checkpoint = build in progress (or was aborted) — clones would fail.
		latest, ok, err := LatestCheckpointFor(ctx, t.Backend, container)
		switch {
		case err != nil:
			v.Status = "failed"
			v.Error = err.Error()
		case !ok:
			v.Status = "building"
		default:
			v.Status = "ready"
			ts := latest.CreatedAt
			v.LastCheckpoint = &ts
		}
		out = append(out, v)
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

	defer t.Locks.Acquire(sb.Name)()

	// Wrap the command in `env [-C cwd] [KEY=val ...] -- <command>` so cwd and
	// env take effect regardless of how the backend handles ExecOpts.Env.
	argv := []string{"env"}
	if in.Cwd != "" {
		argv = append(argv, "-C", in.Cwd)
	}
	keys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		argv = append(argv, k+"="+in.Env[k])
	}
	argv = append(argv, "--")
	argv = append(argv, in.Command...)

	// Backends like SSH space-join argv before sending — the remote shell then
	// re-tokenizes, which silently splits any element containing whitespace or
	// shell metacharacters (e.g. `bash -c "rm -f /path"`). Shell-escape and
	// collapse to a single element so re-tokenization recovers the original argv.
	cmd := []string{shellescape.QuoteCommand(argv)}

	var stdout, stderr strings.Builder
	exit, err := t.Backend.Run(ctx, sb.Name, sandbox.ExecOpts{
		Cmd:    cmd,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	t.touch(sb.Name)

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
	mode, err := parseMode(in.Mode, 0o644)
	if err != nil {
		return WriteFileOut{}, err
	}
	defer t.Locks.Acquire(sb.Name)()

	if err := t.Backend.WriteFile(ctx, sb.Name, in.Path, []byte(in.Content), mode, user.UID, user.GID); err != nil {
		return WriteFileOut{}, err
	}
	t.touch(sb.Name)
	return WriteFileOut{OK: true, BytesWritten: len(in.Content)}, nil
}

func (t *Tools) ReadFile(ctx context.Context, in ReadFileIn) (ReadFileOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ReadFileOut{}, err
	}
	defer t.Locks.Acquire(sb.Name)()

	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = readFileDefaultMaxBytes
	} else if maxBytes > readFileHardMaxBytes {
		maxBytes = readFileHardMaxBytes
	}
	body, truncated, err := t.Backend.ReadFile(ctx, sb.Name, in.Path, maxBytes)
	if err != nil {
		return ReadFileOut{}, err
	}
	t.touch(sb.Name)
	return ReadFileOut{Content: string(body), Truncated: truncated}, nil
}

func (t *Tools) ListFiles(ctx context.Context, in ListFilesIn) (ListFilesOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ListFilesOut{}, err
	}
	defer t.Locks.Acquire(sb.Name)()

	entries, err := t.Backend.ListFiles(ctx, sb.Name, in.Path, in.Recursive)
	if err != nil {
		return ListFilesOut{}, err
	}
	t.touch(sb.Name)
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
	defer t.Locks.Acquire(sb.Name)()

	body, truncated, err := t.Backend.ReadFile(ctx, sb.Name, in.Path, editFileMaxBytes)
	if err != nil {
		return EditFileOut{}, fmt.Errorf("read: %w", err)
	}
	if truncated {
		return EditFileOut{}, fmt.Errorf("file %s exceeds %d bytes; refusing to edit (would truncate trailing content)", in.Path, editFileMaxBytes)
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

	if err := t.Backend.WriteFile(ctx, sb.Name, in.Path, []byte(updated), 0o644, user.UID, user.GID); err != nil {
		return EditFileOut{}, fmt.Errorf("write: %w", err)
	}
	t.touch(sb.Name)
	return EditFileOut{OK: true, Replacements: count}, nil
}

func (t *Tools) DeleteFile(ctx context.Context, in DeleteFileIn) (Ack, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return Ack{}, err
	}
	defer t.Locks.Acquire(sb.Name)()
	if err := t.Backend.DeleteFile(ctx, sb.Name, in.Path); err != nil {
		return Ack{}, err
	}
	t.touch(sb.Name)
	return Ack{OK: true}, nil
}
