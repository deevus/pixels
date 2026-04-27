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
	"time"

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
}

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
	inst, err := t.Backend.Create(ctx, sandbox.CreateOpts{Name: name, Image: image})
	if err != nil {
		return CreateSandboxOut{}, fmt.Errorf("create: %w", err)
	}
	now := time.Now().UTC()
	ip := ""
	if len(inst.Addresses) > 0 {
		ip = inst.Addresses[0]
	}
	t.State.Add(Sandbox{
		Name:           name,
		Label:          in.Label,
		Image:          image,
		IP:             ip,
		Status:         "running",
		CreatedAt:      now,
		LastActivityAt: now,
	})
	if err := t.persist(); err != nil {
		// rollback in-memory state so the next call doesn't see a ghost
		t.State.Remove(name)
		// note: backend container still exists; the agent should retry destroy if they care
		return CreateSandboxOut{}, fmt.Errorf("create %s: state save failed: %w", name, err)
	}
	return CreateSandboxOut{Name: name, IP: ip, Status: "running"}, nil
}

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
			CreatedAt:      sb.CreatedAt,
			LastActivityAt: sb.LastActivityAt,
			IdleFor:        now.Sub(sb.LastActivityAt).Round(time.Second).String(),
		})
	}
	return ListSandboxesOut{Sandboxes: out}, nil
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
