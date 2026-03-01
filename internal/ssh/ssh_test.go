package ssh

import (
	"os"
	"strings"
	"testing"
)

func TestConsole_SSHNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir, no ssh binary
	err := Console("10.0.0.1", "pixel", "", nil)
	if err == nil {
		t.Fatal("expected error when ssh is not on PATH")
	}
	if got := err.Error(); !strings.Contains(got, "ssh binary not found") {
		t.Errorf("error = %q, want it to contain %q", got, "ssh binary not found")
	}
}

func TestSSHArgs(t *testing.T) {
	t.Run("with key", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "/tmp/key", nil)
		wantSuffix := []string{"-i", "/tmp/key", "pixel@10.0.0.1"}
		got := args[len(args)-3:]
		for i, w := range wantSuffix {
			if got[i] != w {
				t.Errorf("args[%d] = %q, want %q", len(args)-3+i, got[i], w)
			}
		}
	})

	t.Run("uses os.DevNull for UserKnownHostsFile", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "", nil)
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
		args := sshArgs("10.0.0.1", "pixel", "", nil)
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

	t.Run("SetEnv with env vars", func(t *testing.T) {
		env := map[string]string{
			"GITHUB_TOKEN": "ghp_abc123",
			"API_KEY":      "sk-secret",
		}
		args := sshArgs("10.0.0.1", "pixel", "", env)

		// All vars should be in a single SetEnv directive (space-separated,
		// sorted by key), preceded by -o. Multiple -o SetEnv flags don't
		// stack in OpenSSH — only the first takes effect.
		want := "SetEnv=API_KEY=sk-secret GITHUB_TOKEN=ghp_abc123"
		var found bool
		for i, a := range args {
			if strings.HasPrefix(a, "SetEnv=") {
				if i == 0 || args[i-1] != "-o" {
					t.Errorf("SetEnv arg %q not preceded by -o", a)
				}
				if a != want {
					t.Errorf("SetEnv = %q, want %q", a, want)
				}
				found = true
			}
		}
		if !found {
			t.Error("SetEnv not found in args")
		}

		// user@host should still be last.
		if last := args[len(args)-1]; last != "pixel@10.0.0.1" {
			t.Errorf("last arg = %q, want %q", last, "pixel@10.0.0.1")
		}
	})

	t.Run("nil env produces no SetEnv", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "", nil)
		for _, a := range args {
			if strings.HasPrefix(a, "SetEnv=") {
				t.Errorf("unexpected SetEnv arg %q with nil env", a)
			}
		}
	})

	t.Run("empty env produces no SetEnv", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "", map[string]string{})
		for _, a := range args {
			if strings.HasPrefix(a, "SetEnv=") {
				t.Errorf("unexpected SetEnv arg %q with empty env", a)
			}
		}
	})
}
