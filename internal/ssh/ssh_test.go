package ssh

import (
	"os"
	"strings"
	"testing"
)

func TestConsole_SSHNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir, no ssh binary
	err := Console(ConnConfig{Host: "10.0.0.1", User: "pixel"}, "")
	if err == nil {
		t.Fatal("expected error when ssh is not on PATH")
	}
	if got := err.Error(); !strings.Contains(got, "ssh binary not found") {
		t.Errorf("error = %q, want it to contain %q", got, "ssh binary not found")
	}
}

func TestSSHArgs(t *testing.T) {
	t.Run("with key", func(t *testing.T) {
		args := Args(ConnConfig{Host: "10.0.0.1", User: "pixel", KeyPath: "/tmp/key"})
		wantSuffix := []string{"-i", "/tmp/key", "pixel@10.0.0.1"}
		got := args[len(args)-3:]
		for i, w := range wantSuffix {
			if got[i] != w {
				t.Errorf("args[%d] = %q, want %q", len(args)-3+i, got[i], w)
			}
		}
	})

	t.Run("uses os.DevNull for UserKnownHostsFile when no KnownHostsFile", func(t *testing.T) {
		args := Args(ConnConfig{Host: "10.0.0.1", User: "pixel"})
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
		// Also verify StrictHostKeyChecking=no in this mode.
		foundStrict := false
		for _, a := range args {
			if a == "StrictHostKeyChecking=no" {
				foundStrict = true
			}
		}
		if !foundStrict {
			t.Errorf("expected StrictHostKeyChecking=no, got %v", args)
		}
	})

	t.Run("accept-new with KnownHostsFile", func(t *testing.T) {
		khFile := "/tmp/pixels-test-known-hosts"
		args := Args(ConnConfig{Host: "10.0.0.1", User: "pixel", KnownHostsFile: khFile})

		// Should use accept-new instead of no.
		foundAcceptNew := false
		for _, a := range args {
			if a == "StrictHostKeyChecking=accept-new" {
				foundAcceptNew = true
			}
			if a == "StrictHostKeyChecking=no" {
				t.Error("should not use StrictHostKeyChecking=no when KnownHostsFile is set")
			}
		}
		if !foundAcceptNew {
			t.Errorf("expected StrictHostKeyChecking=accept-new, got %v", args)
		}

		// Should use the provided known hosts file.
		want := "UserKnownHostsFile=" + khFile
		found := false
		for _, a := range args {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q, got %v", want, args)
		}
	})

	t.Run("without key", func(t *testing.T) {
		args := Args(ConnConfig{Host: "10.0.0.1", User: "pixel"})
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
		cc := ConnConfig{
			Host: "10.0.0.1",
			User: "pixel",
			Env: map[string]string{
				"GITHUB_TOKEN": "ghp_abc123",
				"API_KEY":      "sk-secret",
			},
		}
		args := Args(cc)

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
		args := Args(ConnConfig{Host: "10.0.0.1", User: "pixel"})
		for _, a := range args {
			if strings.HasPrefix(a, "SetEnv=") {
				t.Errorf("unexpected SetEnv arg %q with nil env", a)
			}
		}
	})

	t.Run("empty env produces no SetEnv", func(t *testing.T) {
		args := Args(ConnConfig{Host: "10.0.0.1", User: "pixel", Env: map[string]string{}})
		for _, a := range args {
			if strings.HasPrefix(a, "SetEnv=") {
				t.Errorf("unexpected SetEnv arg %q with empty env", a)
			}
		}
	})
}

func TestRemoveKnownHost(t *testing.T) {
	t.Run("no-op when file is empty string", func(t *testing.T) {
		if err := RemoveKnownHost("", "10.0.0.1"); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("no-op when file does not exist", func(t *testing.T) {
		if err := RemoveKnownHost("/tmp/nonexistent-known-hosts-file", "10.0.0.1"); err != nil {
			t.Errorf("expected no error for missing file, got %v", err)
		}
	})

	t.Run("removes entry from existing file", func(t *testing.T) {
		dir := t.TempDir()
		khFile := dir + "/known_hosts"
		content := "10.0.0.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey\n10.0.0.2 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherKey\n"
		if err := os.WriteFile(khFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := RemoveKnownHost(khFile, "10.0.0.1"); err != nil {
			t.Fatalf("RemoveKnownHost: %v", err)
		}

		data, err := os.ReadFile(khFile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "10.0.0.1") {
			t.Errorf("expected 10.0.0.1 to be removed, file contains: %s", data)
		}
		if !strings.Contains(string(data), "10.0.0.2") {
			t.Errorf("expected 10.0.0.2 to remain, file contains: %s", data)
		}
	})
}

func TestConsoleArgs(t *testing.T) {
	t.Run("no remote command", func(t *testing.T) {
		cc := ConnConfig{Host: "10.0.0.1", User: "pixel", KeyPath: "/tmp/key"}
		args := consoleArgs(cc, "")
		sshA := Args(cc)
		if len(args) != len(sshA) {
			t.Fatalf("len(consoleArgs) = %d, want %d (same as sshArgs)", len(args), len(sshA))
		}
		for i := range args {
			if args[i] != sshA[i] {
				t.Errorf("args[%d] = %q, want %q", i, args[i], sshA[i])
			}
		}
		for _, a := range args {
			if a == "-t" {
				t.Error("should not include -t when remoteCmd is empty")
			}
		}
	})

	t.Run("no key with remote command", func(t *testing.T) {
		cc := ConnConfig{Host: "10.0.0.1", User: "pixel"}
		args := consoleArgs(cc, "zmx attach console")
		last3 := args[len(args)-3:]
		if last3[0] != "-t" {
			t.Errorf("expected -t before user@host, got %q", last3[0])
		}
		if last3[1] != "pixel@10.0.0.1" {
			t.Errorf("expected user@host, got %q", last3[1])
		}
		if last3[2] != "zmx attach console" {
			t.Errorf("expected remote command, got %q", last3[2])
		}
		for _, a := range args {
			if a == "-i" {
				t.Error("should not include -i when keyPath is empty")
			}
		}
	})

	t.Run("with remote command", func(t *testing.T) {
		cc := ConnConfig{Host: "10.0.0.1", User: "pixel", KeyPath: "/tmp/key"}
		args := consoleArgs(cc, "zmx attach console")
		// Should have -t before user@host and command after
		last3 := args[len(args)-3:]
		if last3[0] != "-t" {
			t.Errorf("expected -t before user@host, got %q", last3[0])
		}
		if last3[1] != "pixel@10.0.0.1" {
			t.Errorf("expected user@host, got %q", last3[1])
		}
		if last3[2] != "zmx attach console" {
			t.Errorf("expected remote command, got %q", last3[2])
		}
	})

	t.Run("with env and remote command", func(t *testing.T) {
		cc := ConnConfig{
			Host:    "10.0.0.1",
			User:    "pixel",
			KeyPath: "/tmp/key",
			Env:     map[string]string{"FOO": "bar"},
		}
		args := consoleArgs(cc, "zmx attach build")

		// Verify SetEnv is present.
		var foundSetEnv bool
		for _, a := range args {
			if strings.HasPrefix(a, "SetEnv=") {
				foundSetEnv = true
			}
		}
		if !foundSetEnv {
			t.Error("SetEnv not found in args")
		}

		// Verify -t and command at end.
		last3 := args[len(args)-3:]
		if last3[0] != "-t" {
			t.Errorf("expected -t, got %q", last3[0])
		}
		if last3[1] != "pixel@10.0.0.1" {
			t.Errorf("expected user@host, got %q", last3[1])
		}
		if last3[2] != "zmx attach build" {
			t.Errorf("expected remote command, got %q", last3[2])
		}
	})
}
