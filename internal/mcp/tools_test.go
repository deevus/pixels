package mcp

import (
	"context"
	"os"
	"path/filepath"
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
