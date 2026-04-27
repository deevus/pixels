package sandbox

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeExec captures Run/Output calls.
type fakeExec struct {
	calls      []ExecOpts
	runResp    func(opts ExecOpts) (int, error)
	outputResp func(cmd []string) ([]byte, error)
}

func (f *fakeExec) Run(ctx context.Context, name string, opts ExecOpts) (int, error) {
	f.calls = append(f.calls, opts)
	if f.runResp != nil {
		return f.runResp(opts)
	}
	return 0, nil
}
func (f *fakeExec) Output(ctx context.Context, name string, cmd []string) ([]byte, error) {
	if f.outputResp != nil {
		return f.outputResp(cmd)
	}
	return nil, errors.New("Output not stubbed")
}
func (f *fakeExec) Console(ctx context.Context, name string, opts ConsoleOpts) error { return nil }
func (f *fakeExec) Ready(ctx context.Context, name string, timeout time.Duration) error {
	return nil
}

func TestFilesViaExecWriteFile(t *testing.T) {
	fe := &fakeExec{}
	files := FilesViaExec{Exec: fe}

	if err := files.WriteFile(context.Background(), "sb", "/tmp/foo.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Should have at least 2 calls (mkdir + cat write)
	if got := len(fe.calls); got < 2 {
		t.Fatalf("calls = %d, want >= 2: %+v", got, fe.calls)
	}
	// Find the cat write call and verify Stdin matches content.
	var found bool
	for _, c := range fe.calls {
		if len(c.Cmd) >= 1 && strings.Contains(c.Cmd[0], "sh") && strings.Contains(strings.Join(c.Cmd, " "), "cat") {
			if c.Stdin == nil {
				t.Fatal("write call has nil Stdin")
			}
			b, _ := readAll(c.Stdin)
			if string(b) != "hello" {
				t.Errorf("written content = %q, want %q", b, "hello")
			}
			found = true
		}
	}
	if !found {
		t.Errorf("no cat-write call found in: %+v", fe.calls)
	}
}

func TestFilesViaExecReadFileFull(t *testing.T) {
	fe := &fakeExec{
		runResp: func(opts ExecOpts) (int, error) {
			if opts.Stdout != nil && len(opts.Cmd) > 0 && opts.Cmd[0] == "cat" {
				_, _ = opts.Stdout.Write([]byte("hi"))
			}
			return 0, nil
		},
	}
	files := FilesViaExec{Exec: fe}

	got, truncated, err := files.ReadFile(context.Background(), "sb", "/tmp/f", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if truncated {
		t.Error("truncated should be false when maxBytes==0")
	}
	if string(got) != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestFilesViaExecReadFileTruncatedOnStatError(t *testing.T) {
	fe := &fakeExec{
		runResp: func(opts ExecOpts) (int, error) {
			if opts.Stdout != nil && len(opts.Cmd) > 0 && opts.Cmd[0] == "head" {
				_, _ = opts.Stdout.Write(bytes.Repeat([]byte("x"), 4))
			}
			return 0, nil
		},
		outputResp: func(cmd []string) ([]byte, error) {
			return nil, errors.New("stat: not allowed")
		},
	}
	files := FilesViaExec{Exec: fe}

	got, truncated, err := files.ReadFile(context.Background(), "sb", "/tmp/f", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Errorf("expected truncated=true when stat fails and read length == maxBytes")
	}
	if string(got) != "xxxx" {
		t.Errorf("got %q, want xxxx", got)
	}
}

func TestFilesViaExecReadFileTruncated(t *testing.T) {
	fe := &fakeExec{
		runResp: func(opts ExecOpts) (int, error) {
			if opts.Stdout != nil && len(opts.Cmd) > 0 && opts.Cmd[0] == "head" {
				_, _ = opts.Stdout.Write(bytes.Repeat([]byte("x"), 4))
			}
			return 0, nil
		},
		outputResp: func(cmd []string) ([]byte, error) {
			return []byte("10\n"), nil
		},
	}
	files := FilesViaExec{Exec: fe}

	got, truncated, err := files.ReadFile(context.Background(), "sb", "/tmp/f", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Error("truncated should be true (file=10, maxBytes=4)")
	}
	if string(got) != "xxxx" {
		t.Errorf("got %q, want xxxx", got)
	}
}

func TestFilesViaExecListFilesDoesNotPassDashDashToFind(t *testing.T) {
	var captured []string
	fe := &fakeExec{
		outputResp: func(cmd []string) ([]byte, error) {
			captured = cmd
			return []byte(""), nil
		},
	}
	files := FilesViaExec{Exec: fe}
	_, _ = files.ListFiles(context.Background(), "sb", "/tmp", false)

	for _, arg := range captured {
		if arg == "--" {
			t.Errorf("find argv should not contain --; got %v", captured)
		}
	}
}

func TestFilesViaExecListFiles(t *testing.T) {
	fe := &fakeExec{
		outputResp: func(cmd []string) ([]byte, error) {
			return []byte("/tmp/a.txt\t3\t644\tf\n/tmp/sub\t4096\t755\td\n"), nil
		},
	}
	files := FilesViaExec{Exec: fe}

	entries, err := files.ListFiles(context.Background(), "sb", "/tmp", false)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if got := len(entries); got != 2 {
		t.Fatalf("entries = %d, want 2: %+v", got, entries)
	}
	if entries[1].IsDir != true {
		t.Errorf("entries[1].IsDir = false, want true")
	}
}

func TestFilesViaExecDeleteFile(t *testing.T) {
	fe := &fakeExec{}
	files := FilesViaExec{Exec: fe}

	if err := files.DeleteFile(context.Background(), "sb", "/tmp/f"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if got := len(fe.calls); got < 1 {
		t.Fatalf("no calls recorded")
	}
	cmd := strings.Join(fe.calls[len(fe.calls)-1].Cmd, " ")
	if !strings.HasPrefix(cmd, "rm ") || !strings.Contains(cmd, "/tmp/f") {
		t.Errorf("last cmd = %q, want rm of /tmp/f", cmd)
	}
}
