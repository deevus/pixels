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

type cloneCall struct {
	source, dest string
}

type fakeSandbox struct {
	mu         sync.Mutex
	created    []sandbox.CreateOpts
	deleted    []string
	stopped    []string
	started    []string
	files      map[string][]byte
	fileOwners map[string][2]int // path -> [uid, gid]; populated only when WriteFile got non-default owner
	runHook    func(name string, opts sandbox.ExecOpts) (int, error)
	createHook func(o sandbox.CreateOpts) (*sandbox.Instance, error)
	snapshots  map[string]interface{} // key "<container>:<label>" -> created at (time.Time) OR old format "snapName" -> "ready"
	cloned     []cloneRecord
	containers map[string]sandbox.Instance
	clonedNew  []cloneCall
	runs       [][]string
	deleteErr  error // injected; Delete returns this if non-nil
}

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{
		files:      make(map[string][]byte),
		snapshots:  make(map[string]interface{}),
		containers: make(map[string]sandbox.Instance),
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
	// Check if this container is in the containers map (for test setup).
	if inst, ok := f.containers[n]; ok {
		return &inst, nil
	}
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
func (f *fakeSandbox) Delete(ctx context.Context, n string) error {
	f.deleted = append(f.deleted, n)
	return f.deleteErr
}
func (f *fakeSandbox) CreateSnapshot(ctx context.Context, n, l string) error {
	if f.snapshots == nil {
		f.snapshots = make(map[string]interface{})
	}
	// Store in new format: "<container>:<label>" -> time.Time
	f.snapshots[n+":"+l] = time.Now()
	return nil
}
func (f *fakeSandbox) ListSnapshots(ctx context.Context, n string) ([]sandbox.Snapshot, error) {
	var out []sandbox.Snapshot
	prefix := n + ":"
	for k, v := range f.snapshots {
		if strings.HasPrefix(k, prefix) {
			label := strings.TrimPrefix(k, prefix)
			createdAt := time.Time{}
			if t, ok := v.(time.Time); ok {
				createdAt = t
			}
			out = append(out, sandbox.Snapshot{Label: label, CreatedAt: createdAt})
		}
	}
	return out, nil
}
func (f *fakeSandbox) DeleteSnapshot(ctx context.Context, n, l string) error    { return nil }
func (f *fakeSandbox) RestoreSnapshot(ctx context.Context, n, l string) error   { return nil }
func (f *fakeSandbox) CloneFrom(ctx context.Context, src, lbl, nn string) error {
	f.cloned = append(f.cloned, cloneRecord{source: src, label: lbl, newName: nn})
	f.clonedNew = append(f.clonedNew, cloneCall{source: src, dest: nn})
	if f.containers == nil {
		f.containers = make(map[string]sandbox.Instance)
	}
	f.containers[nn] = sandbox.Instance{Name: nn, Status: sandbox.StatusRunning}
	return nil
}
func (f *fakeSandbox) Run(ctx context.Context, n string, o sandbox.ExecOpts) (int, error) {
	f.runs = append(f.runs, o.Cmd)
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
func (f *fakeSandbox) WriteFile(ctx context.Context, name, path string, content []byte, mode os.FileMode, uid, gid int) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	f.files[path] = cp
	if uid >= 0 && gid >= 0 {
		if f.fileOwners == nil {
			f.fileOwners = map[string][2]int{}
		}
		f.fileOwners[path] = [2]int{uid, gid}
	}
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
	// python base container exists with snapshot; node does not.
	// Prefix is empty in this Cfg → BaseName falls back to DefaultBasePrefix ("base-").
	fb.created = append(fb.created, sandbox.CreateOpts{Name: "base-python"})
	fb.snapshots["base-python:"+InitialCheckpointLabel] = time.Now()

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

func TestListBasesReportsBuildingWhenContainerHasNoCheckpoint(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			Bases: map[string]config.Base{
				"dev": {ParentImage: "ubuntu/24.04", Description: "Dev"},
			},
		},
	}
	tt.Builder = &Builder{}
	// Container exists (created during build) but no checkpoint yet (script
	// still running). Status should be "building", not "ready".
	fb.created = append(fb.created, sandbox.CreateOpts{Name: "base-dev"})

	out, err := tt.ListBases(context.Background(), EmptyIn{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range out.Bases {
		got[b.Name] = b.Status
	}
	if got["dev"] != "building" {
		t.Errorf("dev status = %q, want building", got["dev"])
	}
}

func TestListBasesIncludesFromAndLastCheckpoint(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", Description: "dev"},
				"python": {From: "dev", Description: "python"},
			},
		},
	}

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	fb.containers = map[string]sandbox.Instance{
		"px-base-dev":    {Name: "px-base-dev", Status: sandbox.StatusStopped},
		"px-base-python": {Name: "px-base-python", Status: sandbox.StatusStopped},
	}
	fb.snapshots = map[string]interface{}{
		"px-base-dev:initial":     now.Add(-2 * time.Hour),
		"px-base-python:initial":  now.Add(-1 * time.Hour),
		"px-base-python:custom":   now,
	}

	out, err := tt.ListBases(context.Background(), EmptyIn{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]BaseView{}
	for _, b := range out.Bases {
		byName[b.Name] = b
	}

	py := byName["python"]
	if py.From != "dev" {
		t.Errorf("python.From = %q, want dev", py.From)
	}
	if py.LastCheckpoint == nil || !py.LastCheckpoint.Equal(now) {
		t.Errorf("python.LastCheckpoint = %v, want %v", py.LastCheckpoint, now)
	}
	if py.Status != "ready" {
		t.Errorf("python.Status = %q, want ready", py.Status)
	}

	dev := byName["dev"]
	if dev.From != "" {
		t.Errorf("dev.From should be empty, got %q", dev.From)
	}
	if dev.ParentImage != "images:ubuntu/24.04" {
		t.Errorf("dev.ParentImage = %q", dev.ParentImage)
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
		return BuildBase(ctx, tt.Backend, tt.Cfg, name, tt.Cfg.MCP.Bases[name], BuildBaseOpts{Out: io.Discard})
	}

	// Pretend the snapshot already exists for this test.
	fb.snapshots[BaseName(tt.Cfg, "python")+":"+InitialCheckpointLabel] = time.Now()

	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Base: "python"})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for provisioning to flip to running.
	mustEventually(t, func() bool {
		got, _ := tt.State.Get(out.Name)
		return got.Status == "running"
	})
	// Verify CloneFrom was used with the base container.
	if len(fb.cloned) != 1 || fb.cloned[0].source != BaseName(tt.Cfg, "python") || fb.cloned[0].label != InitialCheckpointLabel {
		t.Errorf("expected CloneFrom base %s snap %s; got %+v", BaseName(tt.Cfg, "python"), InitialCheckpointLabel, fb.cloned)
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
			return BuildBase(ctx, tt.Backend, tt.Cfg, name, tt.Cfg.MCP.Bases[name], BuildBaseOpts{Out: io.Discard})
		},
	}

	// Arrange: base container already exists in fake backend state (via created).
	fb.created = append(fb.created, sandbox.CreateOpts{Name: BaseName(tt.Cfg, "python")})
	fb.snapshots[BaseName(tt.Cfg, "python")+":"+InitialCheckpointLabel] = time.Now()

	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Base: "python"})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for provisioning to flip to running.
	mustEventually(t, func() bool {
		got, _ := tt.State.Get(out.Name)
		return got.Status == "running"
	})
	// Assert Builder.Build was NOT called (= 0) since base already exists.
	if buildCalls != 0 {
		t.Errorf("Builder.Build called %d times; expected 0 (should skip when base exists)", buildCalls)
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

func TestWriteFileChownsToPixel(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if _, err := tt.WriteFile(context.Background(), WriteFileIn{
		Name:    out.Name,
		Path:    "/home/pixel/x",
		Content: "data",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := be.fileOwners["/home/pixel/x"]
	if !ok {
		t.Fatal("WriteFile must request explicit ownership for MCP tools")
	}
	if want := [2]int{pixelUID, pixelGID}; got != want {
		t.Errorf("owner = %v, want %v (MCP must always write as the pixel user)", got, want)
	}
}

func TestEditFileChownsToPixel(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/home/pixel/main.py"] = []byte("hi")
	delete(be.fileOwners, "/home/pixel/main.py")
	if _, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/home/pixel/main.py",
		OldString: "hi",
		NewString: "bye",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := be.fileOwners["/home/pixel/main.py"]
	if !ok {
		t.Fatal("EditFile must request explicit ownership when writing back")
	}
	if want := [2]int{pixelUID, pixelGID}; got != want {
		t.Errorf("owner = %v, want %v", got, want)
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

func TestListSandboxesReconcilesFailedToRunning(t *testing.T) {
	tt, fb := newTestTools(t)
	// State says failed; backend says container is RUNNING with an IP.
	// list_sandboxes should reconcile and report running.
	tt.State.Add(Sandbox{Name: "mcp-recovered", Status: "failed", Error: "ready: no IP address for mcp-recovered"})
	fb.containers = map[string]sandbox.Instance{
		"mcp-recovered": {Name: "mcp-recovered", Status: sandbox.StatusRunning, Addresses: []string{"192.168.1.108"}},
	}

	out, err := tt.ListSandboxes(context.Background(), EmptyIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sandboxes) != 1 {
		t.Fatalf("got %d sandboxes, want 1", len(out.Sandboxes))
	}
	got := out.Sandboxes[0]
	if got.Status != "running" {
		t.Errorf("Status = %q, want running", got.Status)
	}
	if got.IP != "192.168.1.108" {
		t.Errorf("IP = %q, want 192.168.1.108", got.IP)
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty after recovery", got.Error)
	}
}

func TestListSandboxesCachesBackendQueries(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.State.Add(Sandbox{Name: "mcp-x", Status: "running"})
	fb.containers = map[string]sandbox.Instance{
		"mcp-x": {Name: "mcp-x", Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}},
	}

	// First call populates the cache.
	if _, err := tt.ListSandboxes(context.Background(), EmptyIn{}); err != nil {
		t.Fatal(err)
	}
	// Mutate backend in a way that would change reconciled status.
	fb.mu.Lock()
	fb.containers["mcp-x"] = sandbox.Instance{Name: "mcp-x", Status: sandbox.StatusStopped}
	fb.mu.Unlock()

	// Within the TTL window, the second call should still see "running"
	// because reconciliation is cached.
	out, err := tt.ListSandboxes(context.Background(), EmptyIn{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Sandboxes[0].Status != "running" {
		t.Errorf("Status = %q, want running (cached)", out.Sandboxes[0].Status)
	}

	// Force the cache to expire and call again — now it should reconcile.
	tt.reconcileMu.Lock()
	tt.lastSync = time.Now().Add(-time.Hour)
	tt.reconcileMu.Unlock()

	out, err = tt.ListSandboxes(context.Background(), EmptyIn{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Sandboxes[0].Status != "stopped" {
		t.Errorf("Status = %q, want stopped (after cache expiry)", out.Sandboxes[0].Status)
	}
}

func TestDestroySandboxRemovesGhostState(t *testing.T) {
	tt, be := newTestTools(t)
	// Pre-seed a state record without a corresponding backend instance — a ghost.
	tt.State.MarkRunning("mcp-ghost")
	be.deleteErr = sandbox.WrapNotFound(errors.New("does not exist"))

	if _, err := tt.DestroySandbox(context.Background(), SandboxRef{Name: "mcp-ghost"}); err != nil {
		t.Fatalf("DestroySandbox should treat ErrNotFound as success; got %v", err)
	}
	if _, ok := tt.State.Get("mcp-ghost"); ok {
		t.Errorf("ghost state record should be removed even when backend reports ErrNotFound")
	}
}

