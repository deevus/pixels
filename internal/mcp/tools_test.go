package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deevus/pixels/sandbox"
)

type fakeSandbox struct {
	created []sandbox.CreateOpts
	deleted []string
	stopped []string
	started []string
	files   map[string][]byte
	runHook func(name string, opts sandbox.ExecOpts) (int, error)
}

func newFakeSandbox() *fakeSandbox { return &fakeSandbox{files: make(map[string][]byte)} }

func (f *fakeSandbox) Create(ctx context.Context, o sandbox.CreateOpts) (*sandbox.Instance, error) {
	f.created = append(f.created, o)
	return &sandbox.Instance{Name: o.Name, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) Get(ctx context.Context, n string) (*sandbox.Instance, error) {
	return &sandbox.Instance{Name: n, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) List(ctx context.Context) ([]sandbox.Instance, error) { return nil, nil }
func (f *fakeSandbox) Start(ctx context.Context, n string) error            { f.started = append(f.started, n); return nil }
func (f *fakeSandbox) Stop(ctx context.Context, n string) error             { f.stopped = append(f.stopped, n); return nil }
func (f *fakeSandbox) Delete(ctx context.Context, n string) error           { f.deleted = append(f.deleted, n); return nil }
func (f *fakeSandbox) CreateSnapshot(ctx context.Context, n, l string) error { return nil }
func (f *fakeSandbox) ListSnapshots(ctx context.Context, n string) ([]sandbox.Snapshot, error) {
	return nil, nil
}
func (f *fakeSandbox) DeleteSnapshot(ctx context.Context, n, l string) error    { return nil }
func (f *fakeSandbox) RestoreSnapshot(ctx context.Context, n, l string) error   { return nil }
func (f *fakeSandbox) CloneFrom(ctx context.Context, src, lbl, nn string) error { return nil }
func (f *fakeSandbox) Run(ctx context.Context, n string, o sandbox.ExecOpts) (int, error) {
	if f.runHook != nil {
		return f.runHook(n, o)
	}
	return 0, nil
}
func (f *fakeSandbox) Output(ctx context.Context, n string, c []string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSandbox) Console(ctx context.Context, n string, o sandbox.ConsoleOpts) error { return nil }
func (f *fakeSandbox) Ready(ctx context.Context, n string, t time.Duration) error         { return nil }
func (f *fakeSandbox) SetEgressMode(ctx context.Context, n string, m sandbox.EgressMode) error {
	return nil
}
func (f *fakeSandbox) AllowDomain(ctx context.Context, n, d string) error { return nil }
func (f *fakeSandbox) DenyDomain(ctx context.Context, n, d string) error  { return nil }
func (f *fakeSandbox) GetPolicy(ctx context.Context, n string) (*sandbox.Policy, error) {
	return nil, nil
}
func (f *fakeSandbox) Capabilities() sandbox.Capabilities { return sandbox.Capabilities{} }
func (f *fakeSandbox) Close() error                       { return nil }

// errBackend wraps a fakeSandbox and returns an error from Run.
type errBackend struct {
	*fakeSandbox
	runErr error
}

func (e *errBackend) Run(ctx context.Context, name string, opts sandbox.ExecOpts) (int, error) {
	if opts.Stdout != nil {
		_, _ = opts.Stdout.Write([]byte("partial-stdout"))
	}
	return 137, e.runErr
}

// Files capability (in-memory implementations so EditFile etc. round-trip).
func (f *fakeSandbox) WriteFile(ctx context.Context, name, path string, content []byte, mode os.FileMode) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	f.files[path] = cp
	return nil
}
func (f *fakeSandbox) ReadFile(ctx context.Context, name, path string, maxBytes int64) ([]byte, bool, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, false, os.ErrNotExist
	}
	if maxBytes > 0 && int64(len(b)) > maxBytes {
		return b[:maxBytes], true, nil
	}
	return b, false, nil
}
func (f *fakeSandbox) ListFiles(ctx context.Context, name, path string, recursive bool) ([]sandbox.FileEntry, error) {
	return nil, nil
}
func (f *fakeSandbox) DeleteFile(ctx context.Context, name, path string) error {
	delete(f.files, path)
	return nil
}

func newTestTools(t *testing.T) (*Tools, *fakeSandbox) {
	t.Helper()
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	be := newFakeSandbox()
	return &Tools{
		State:          s,
		Backend:        be,
		Prefix:         "px-mcp-",
		DefaultImage:   "ubuntu/24.04",
		ExecTimeoutMax: 10 * time.Minute,
		Log:            NopLogger(),
		Locks:          &SandboxLocks{},
	}, be
}

func TestCreateSandboxPropagatesSaveError(t *testing.T) {
	tt, _ := newTestTools(t)
	// Force Save() to fail by using an unwritable path.
	tt.State.SetPathForTest("/nonexistent/dir/state.json")

	_, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if err == nil {
		t.Fatal("expected CreateSandbox to surface the save error")
	}
	if got := len(tt.State.Sandboxes()); got != 0 {
		t.Errorf("state should be empty after save failure; got %d", got)
	}
}

func TestExecAppliesCwd(t *testing.T) {
	tt, fb := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})

	var capturedCmd []string
	fb.runHook = func(name string, opts sandbox.ExecOpts) (int, error) {
		capturedCmd = opts.Cmd
		return 0, nil
	}

	_, err := tt.Exec(context.Background(), ExecIn{
		Name:    out.Name,
		Command: []string{"pwd"},
		Cwd:     "/tmp",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"env", "-C", "/tmp", "--", "pwd"}
	if !reflect.DeepEqual(capturedCmd, want) {
		t.Errorf("exec argv = %v, want %v", capturedCmd, want)
	}
}

func TestExecAppliesEnv(t *testing.T) {
	tt, fb := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})

	var capturedCmd []string
	fb.runHook = func(name string, opts sandbox.ExecOpts) (int, error) {
		capturedCmd = opts.Cmd
		return 0, nil
	}

	_, err := tt.Exec(context.Background(), ExecIn{
		Name:    out.Name,
		Command: []string{"sh", "-c", "echo $X"},
		Env:     map[string]string{"X": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := capturedCmd[0], "env"; got != want {
		t.Errorf("argv[0] = %q, want %q", got, want)
	}
	// Assert "X=hello" is somewhere in argv before "--".
	foundX := false
	for _, a := range capturedCmd {
		if a == "--" {
			break
		}
		if a == "X=hello" {
			foundX = true
		}
	}
	if !foundX {
		t.Errorf("env var X=hello missing from argv: %v", capturedCmd)
	}
}

func TestExecReportsTransportError(t *testing.T) {
	tt, _ := newTestTools(t)
	tt.Backend = &errBackend{
		fakeSandbox: tt.Backend.(*fakeSandbox),
		runErr:      errors.New("ssh connection lost"),
	}
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})

	res, err := tt.Exec(context.Background(), ExecIn{
		Name:    out.Name,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Exec returned err=%v; expected the error in the response field instead", err)
	}
	if res.TransportError == "" {
		t.Errorf("expected non-empty TransportError; got %+v", res)
	}
	if res.ExitCode != 137 {
		t.Errorf("expected ExitCode propagated; got %d", res.ExitCode)
	}
	if res.Stdout != "partial-stdout" {
		t.Errorf("expected partial Stdout preserved; got %q", res.Stdout)
	}
}

func TestCreateSandboxAddsState(t *testing.T) {
	tt, be := newTestTools(t)
	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Label: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name == "" || out.Status != "running" {
		t.Errorf("unexpected out: %+v", out)
	}
	if len(be.created) != 1 || be.created[0].Image != "ubuntu/24.04" {
		t.Errorf("unexpected create call: %+v", be.created)
	}
	if _, ok := tt.State.Get(out.Name); !ok {
		t.Error("sandbox not added to state")
	}
}

func TestDestroyRemovesState(t *testing.T) {
	tt, _ := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if _, err := tt.DestroySandbox(context.Background(), SandboxRef{Name: out.Name}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.State.Get(out.Name); ok {
		t.Error("sandbox should be removed from state")
	}
}

func TestEditFileReplacesUnique(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/app/main.py"] = []byte("print('hi')\n")

	res, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/app/main.py",
		OldString: "hi",
		NewString: "hello",
	})
	if err != nil {
		t.Fatalf("EditFile: %v", err)
	}
	if res.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", res.Replacements)
	}
	if got := string(be.files["/app/main.py"]); got != "print('hello')\n" {
		t.Errorf("file content = %q", got)
	}
}

func TestEditFileMissingErrors(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("abc")
	_, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/x",
		OldString: "z",
		NewString: "y",
	})
	if err == nil {
		t.Fatal("expected error for missing old_string")
	}
}

func TestEditFileNonUniqueErrors(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("aaa")
	_, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/x",
		OldString: "a",
		NewString: "b",
	})
	if err == nil {
		t.Fatal("expected error for non-unique old_string without replace_all")
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("aaa")
	res, err := tt.EditFile(context.Background(), EditFileIn{
		Name:       out.Name,
		Path:       "/x",
		OldString:  "a",
		NewString:  "b",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replacements != 3 {
		t.Errorf("Replacements = %d, want 3", res.Replacements)
	}
	if string(be.files["/x"]) != "bbb" {
		t.Errorf("content = %q", be.files["/x"])
	}
}

func TestDeleteFileRemoves(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("data")
	if _, err := tt.DeleteFile(context.Background(), DeleteFileIn{Name: out.Name, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := be.files["/x"]; ok {
		t.Error("file should be deleted")
	}
}
