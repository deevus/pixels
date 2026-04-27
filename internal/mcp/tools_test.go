package mcp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
)

type cloneRecord struct {
	source  string
	label   string
	newName string
}

type fakeSandbox struct {
	mu         sync.Mutex
	created    []sandbox.CreateOpts
	deleted    []string
	stopped    []string
	started    []string
	files      map[string][]byte
	runHook    func(name string, opts sandbox.ExecOpts) (int, error)
	createHook func(o sandbox.CreateOpts) (*sandbox.Instance, error)
	snapshots  map[string]string // snapName → ready
	cloned     []cloneRecord
}

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{
		files:     make(map[string][]byte),
		snapshots: make(map[string]string),
	}
}

func (f *fakeSandbox) lenCreated() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *fakeSandbox) getCreated(i int) sandbox.CreateOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created[i]
}

func (f *fakeSandbox) Create(ctx context.Context, o sandbox.CreateOpts) (*sandbox.Instance, error) {
	f.mu.Lock()
	f.created = append(f.created, o)
	f.mu.Unlock()
	if f.createHook != nil {
		return f.createHook(o)
	}
	return &sandbox.Instance{Name: o.Name, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) Get(ctx context.Context, n string) (*sandbox.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Check if this container was created.
	for _, c := range f.created {
		if c.Name == n {
			return &sandbox.Instance{Name: n, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
		}
	}
	// For builder containers (px-base-builder-*), check if any snapshot for that base exists.
	if strings.HasPrefix(n, "px-base-builder-") {
		baseName := strings.TrimPrefix(n, "px-base-builder-")
		// Check if any snapshot exists for this base (e.g., "px-base-python").
		for snapName := range f.snapshots {
			if strings.Contains(snapName, baseName) {
				return &sandbox.Instance{Name: n, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
			}
		}
	}
	return nil, sandbox.ErrNotFound
}
func (f *fakeSandbox) List(ctx context.Context) ([]sandbox.Instance, error) { return nil, nil }
func (f *fakeSandbox) Start(ctx context.Context, n string) error            { f.started = append(f.started, n); return nil }
func (f *fakeSandbox) Stop(ctx context.Context, n string) error             { f.stopped = append(f.stopped, n); return nil }
func (f *fakeSandbox) Delete(ctx context.Context, n string) error           { f.deleted = append(f.deleted, n); return nil }
func (f *fakeSandbox) CreateSnapshot(ctx context.Context, n, l string) error {
	if f.snapshots == nil {
		f.snapshots = make(map[string]string)
	}
	f.snapshots[l] = "ready"
	return nil
}
func (f *fakeSandbox) ListSnapshots(ctx context.Context, n string) ([]sandbox.Snapshot, error) {
	return nil, nil
}
func (f *fakeSandbox) DeleteSnapshot(ctx context.Context, n, l string) error    { return nil }
func (f *fakeSandbox) RestoreSnapshot(ctx context.Context, n, l string) error   { return nil }
func (f *fakeSandbox) CloneFrom(ctx context.Context, src, lbl, nn string) error {
	f.cloned = append(f.cloned, cloneRecord{source: src, label: lbl, newName: nn})
	return nil
}
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

	ctx, cancel := context.WithCancel(context.Background())
	tt := &Tools{
		State:          s,
		Backend:        be,
		Prefix:         "px-mcp-",
		DefaultImage:   "ubuntu/24.04",
		ExecTimeoutMax: 10 * time.Minute,
		Log:            NopLogger(),
		Locks:          &SandboxLocks{},
		DaemonCtx:      ctx,
	}
	t.Cleanup(func() {
		cancel()
		tt.provisionWG.Wait()
	})
	return tt, be
}

func mustEventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if fn() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition never became true")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestListBasesReturnsConfiguredBases(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			Bases: map[string]config.Base{
				"python": {ParentImage: "images:ubuntu/24.04", Description: "Python 3"},
				"node":   {ParentImage: "images:ubuntu/24.04", Description: "Node 22"},
			},
		},
	}
	tt.Builder = &Builder{}
	fb.snapshots["px-base-python"] = "ready" // python is built; node is not

	out, err := tt.ListBases(context.Background(), EmptyIn{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range out.Bases {
		got[b.Name] = b.Status
	}
	if got["python"] != "ready" {
		t.Errorf("python status = %q, want ready", got["python"])
	}
	if got["node"] != "missing" {
		t.Errorf("node status = %q, want missing", got["node"])
	}
}

func TestListSandboxesIncludesErrorAndIP(t *testing.T) {
	tt, _ := newTestTools(t)
	tt.State.Add(Sandbox{
		Name:   "fail",
		Status: "failed",
		Error:  "boom",
	})
	tt.State.Add(Sandbox{
		Name:   "run",
		Status: "running",
		IP:     "10.0.0.5",
	})

	out, _ := tt.ListSandboxes(context.Background(), EmptyIn{})
	var fail, run *SandboxView
	for i := range out.Sandboxes {
		if out.Sandboxes[i].Name == "fail" {
			fail = &out.Sandboxes[i]
		}
		if out.Sandboxes[i].Name == "run" {
			run = &out.Sandboxes[i]
		}
	}
	if fail == nil || fail.Error != "boom" {
		t.Errorf("expected error=boom on fail; got %+v", fail)
	}
	if run == nil || run.IP != "10.0.0.5" {
		t.Errorf("expected ip=10.0.0.5 on run; got %+v", run)
	}
}

func writeTempScript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCreateSandboxWithBaseClonesFromSnapshot(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			Bases: map[string]config.Base{
				"python": {
					ParentImage: "images:ubuntu/24.04",
					SetupScript: writeTempScript(t, "#!/bin/bash\necho hi\n"),
					Description: "Python 3",
				},
			},
		},
	}
	tt.BuildLockDir = t.TempDir()
	tt.Builder = &Builder{}
	tt.Builder.DoBuild = func(ctx context.Context, name string) error {
		return BuildBase(ctx, tt.Backend, tt.Cfg.MCP.Bases[name], name, io.Discard)
	}

	// Pretend the snapshot already exists for this test.
	fb.snapshots[SnapshotName("python")] = "ready"

	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Base: "python"})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for provisioning to flip to running.
	mustEventually(t, func() bool {
		got, _ := tt.State.Get(out.Name)
		return got.Status == "running"
	})
	// Verify CloneFrom was used with the builder container, not Create.
	if len(fb.cloned) != 1 || fb.cloned[0].source != BuilderContainerName("python") || fb.cloned[0].label != SnapshotName("python") {
		t.Errorf("expected CloneFrom builder %s snap %s; got %+v", BuilderContainerName("python"), SnapshotName("python"), fb.cloned)
	}
}

func TestCreateSandboxSkipsBuildWhenBuilderExists(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			Bases: map[string]config.Base{
				"python": {
					ParentImage: "images:ubuntu/24.04",
					SetupScript: writeTempScript(t, "#!/bin/bash\necho hi\n"),
				},
			},
		},
	}
	tt.BuildLockDir = t.TempDir()
	var buildCalls int
	tt.Builder = &Builder{
		DoBuild: func(ctx context.Context, name string) error {
			buildCalls++
			return BuildBase(ctx, tt.Backend, tt.Cfg.MCP.Bases[name], name, io.Discard)
		},
	}

	// Arrange: builder container already exists in fake backend state (via created).
	fb.created = append(fb.created, sandbox.CreateOpts{Name: BuilderContainerName("python")})
	fb.snapshots[SnapshotName("python")] = "ready"

	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Base: "python"})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for provisioning to flip to running.
	mustEventually(t, func() bool {
		got, _ := tt.State.Get(out.Name)
		return got.Status == "running"
	})
	// Assert Builder.Build was NOT called (= 0).
	if buildCalls != 0 {
		t.Errorf("Builder.Build called %d times; expected 0 (should skip when builder exists)", buildCalls)
	}
}

func TestCreateSandboxReturnsImmediatelyWithProvisioning(t *testing.T) {
	ctx := context.Background()
	tt, fb := newTestTools(t)

	// Stub the backend's Create to block — simulates a slow provision.
	createReturn := make(chan struct{})
	fb.createHook = func(o sandbox.CreateOpts) (*sandbox.Instance, error) {
		<-createReturn
		return &sandbox.Instance{Name: o.Name, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
	}

	tt.DaemonCtx = ctx

	start := time.Now()
	out, err := tt.CreateSandbox(ctx, CreateSandboxIn{})
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Since(start); got > 100*time.Millisecond {
		t.Errorf("CreateSandbox took %v; should return immediately", got)
	}
	if out.Status != "provisioning" {
		t.Errorf("Status = %q, want provisioning", out.Status)
	}

	// Unblock the backend; goroutine should now flip status to running.
	close(createReturn)
	mustEventually(t, func() bool {
		got, _ := tt.State.Get(out.Name)
		return got.Status == "running"
	})
}

func TestCreateSandboxPropagatesSaveError(t *testing.T) {
	tt, be := newTestTools(t)
	// Force Save() to fail by using an unwritable path.
	tt.State.SetPathForTest("/nonexistent/dir/state.json")

	_, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if err == nil {
		t.Fatal("expected CreateSandbox to surface the save error")
	}
	if got := len(tt.State.Sandboxes()); got != 0 {
		t.Errorf("state should be empty after save failure; got %d", got)
	}
	// With async provisioning, no backend call happens synchronously.
	// The save failure prevents the goroutine from launching.
	if n := be.lenCreated(); n != 0 {
		t.Errorf("backend.Create should not have been called synchronously; got %d", n)
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
	if out.Name == "" || out.Status != "provisioning" {
		t.Errorf("unexpected out: %+v", out)
	}
	// Wait for provisioning to finish.
	mustEventually(t, func() bool {
		return be.lenCreated() >= 1
	})
	if be.getCreated(0).Image != "ubuntu/24.04" {
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

