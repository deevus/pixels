package truenas

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/ssh"
)

func newFilesTestBackend(t *testing.T, mssh *mockSSH) *TrueNAS {
	t.Helper()
	tn, err := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
		},
	}, mssh, testCfg())
	if err != nil {
		t.Fatalf("NewForTest: %v", err)
	}
	return tn
}

func TestReadFileFull(t *testing.T) {
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			if len(cmd) == 0 || cmd[0] != "cat" {
				t.Errorf("expected cat command, got %v", cmd)
			}
			return []byte("hi"), nil
		},
	}
	tn := newFilesTestBackend(t, mssh)

	got, truncated, err := tn.ReadFile(context.Background(), "test", "/tmp/f", 0)
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

func TestReadFileTruncated(t *testing.T) {
	calls := 0
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			calls++
			if cmd[0] == "head" {
				return bytes.Repeat([]byte("x"), 4), nil
			}
			if cmd[0] == "stat" {
				return []byte("10\n"), nil
			}
			t.Fatalf("unexpected cmd: %v", cmd)
			return nil, nil
		},
	}
	tn := newFilesTestBackend(t, mssh)

	got, truncated, err := tn.ReadFile(context.Background(), "test", "/tmp/f", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Error("truncated should be true (file=10, maxBytes=4)")
	}
	if string(got) != "xxxx" {
		t.Errorf("got %q, want xxxx", got)
	}
	if calls != 2 {
		t.Errorf("expected 2 Output calls (head + stat), got %d", calls)
	}
}

func TestReadFileTruncatedOnStatError(t *testing.T) {
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			if cmd[0] == "head" {
				return bytes.Repeat([]byte("x"), 4), nil
			}
			return nil, errors.New("stat: not allowed")
		},
	}
	tn := newFilesTestBackend(t, mssh)

	got, truncated, err := tn.ReadFile(context.Background(), "test", "/tmp/f", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Error("expected truncated=true when stat fails and read length == maxBytes")
	}
	if string(got) != "xxxx" {
		t.Errorf("got %q, want xxxx", got)
	}
}

func TestListFiles(t *testing.T) {
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			return []byte("/tmp/a.txt\t3\t644\tf\n/tmp/sub\t4096\t755\td\n"), nil
		},
	}
	tn := newFilesTestBackend(t, mssh)

	entries, err := tn.ListFiles(context.Background(), "test", "/tmp", false)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if got := len(entries); got != 2 {
		t.Fatalf("entries = %d, want 2: %+v", got, entries)
	}
	if entries[0].Path != "/tmp/a.txt" || entries[0].Size != 3 || entries[0].IsDir {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if !entries[1].IsDir {
		t.Errorf("entries[1].IsDir = false, want true")
	}
}

func TestListFilesNonRecursiveUsesMaxdepth1(t *testing.T) {
	var captured []string
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			captured = cmd
			return nil, nil
		},
	}
	tn := newFilesTestBackend(t, mssh)

	_, _ = tn.ListFiles(context.Background(), "test", "/tmp", false)
	if !contains(captured, "-maxdepth") {
		t.Errorf("non-recursive ListFiles must pass -maxdepth 1; got %v", captured)
	}
}

func TestListFilesRecursiveOmitsMaxdepth(t *testing.T) {
	var captured []string
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			captured = cmd
			return nil, nil
		},
	}
	tn := newFilesTestBackend(t, mssh)

	_, _ = tn.ListFiles(context.Background(), "test", "/tmp", true)
	if contains(captured, "-maxdepth") {
		t.Errorf("recursive ListFiles must not pass -maxdepth; got %v", captured)
	}
}

func TestDeleteFile(t *testing.T) {
	mssh := &mockSSH{}
	tn := newFilesTestBackend(t, mssh)

	if err := tn.DeleteFile(context.Background(), "test", "/tmp/f"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if len(mssh.execCalls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(mssh.execCalls))
	}
	cmd := strings.Join(mssh.execCalls[0].Cmd, " ")
	if !strings.HasPrefix(cmd, "rm ") || !strings.Contains(cmd, "/tmp/f") {
		t.Errorf("cmd = %q, want rm of /tmp/f", cmd)
	}
}

func TestDeleteFileExitNonZero(t *testing.T) {
	mssh := &mockSSH{
		execFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
			return 1, nil
		},
	}
	tn := newFilesTestBackend(t, mssh)

	err := tn.DeleteFile(context.Background(), "test", "/tmp/f")
	if err == nil {
		t.Fatal("expected error when rm exits non-zero")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("error = %q, want it to mention exit 1", err.Error())
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
