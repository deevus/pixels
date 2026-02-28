package ssh

import (
	"os"
	"strings"
	"testing"
)

func TestConsole_SSHNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir, no ssh binary
	err := Console("10.0.0.1", "pixel", "")
	if err == nil {
		t.Fatal("expected error when ssh is not on PATH")
	}
	if got := err.Error(); !strings.Contains(got, "ssh binary not found") {
		t.Errorf("error = %q, want it to contain %q", got, "ssh binary not found")
	}
}

func TestSSHArgs(t *testing.T) {
	t.Run("with key", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "/tmp/key")
		wantSuffix := []string{"-i", "/tmp/key", "pixel@10.0.0.1"}
		got := args[len(args)-3:]
		for i, w := range wantSuffix {
			if got[i] != w {
				t.Errorf("args[%d] = %q, want %q", len(args)-3+i, got[i], w)
			}
		}
	})

	t.Run("uses os.DevNull for UserKnownHostsFile", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "")
		want := "UserKnownHostsFile=" + os.DevNull
		found := false
		for _, a := range args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("sshArgs should contain %q, got %v", want, args)
		}
	})

	t.Run("without key", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "")
		last := args[len(args)-1]
		if last != "pixel@10.0.0.1" {
			t.Errorf("last arg = %q, want %q", last, "pixel@10.0.0.1")
		}
		for _, a := range args {
			if a == "-i" {
				t.Error("should not include -i when keyPath is empty")
			}
		}
	})
}
