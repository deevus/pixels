package provision

import (
	"strings"
	"testing"
)

func TestZmxCmd(t *testing.T) {
	got := zmxCmd("zmx list")
	want := "unset XDG_RUNTIME_DIR && zmx list"
	if got != want {
		t.Errorf("zmxCmd(\"zmx list\") = %q, want %q", got, want)
	}
}

func TestRunnerConn(t *testing.T) {
	r := Runner{Host: "10.0.0.1", User: "root", KeyPath: "/tmp/key"}
	cc := r.conn()
	if cc.Host != "10.0.0.1" {
		t.Errorf("Host = %q, want %q", cc.Host, "10.0.0.1")
	}
	if cc.User != "root" {
		t.Errorf("User = %q, want %q", cc.User, "root")
	}
	if cc.KeyPath != "/tmp/key" {
		t.Errorf("KeyPath = %q, want %q", cc.KeyPath, "/tmp/key")
	}
}

func TestSteps(t *testing.T) {
	tests := []struct {
		name      string
		egress    string
		devtools  bool
		wantNames []string
	}{
		{
			name:      "no egress no devtools",
			egress:    "unrestricted",
			devtools:  false,
			wantNames: nil,
		},
		{
			name:      "devtools only",
			egress:    "unrestricted",
			devtools:  true,
			wantNames: []string{"px-devtools"},
		},
		{
			name:      "egress agent only",
			egress:    "agent",
			devtools:  false,
			wantNames: []string{"px-egress"},
		},
		{
			name:      "egress allowlist only",
			egress:    "allowlist",
			devtools:  false,
			wantNames: []string{"px-egress"},
		},
		{
			name:      "egress and devtools",
			egress:    "agent",
			devtools:  true,
			wantNames: []string{"px-devtools", "px-egress"},
		},
		{
			name:      "empty egress string",
			egress:    "",
			devtools:  false,
			wantNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := Steps(tt.egress, tt.devtools)
			names := StepNames(steps)

			if len(names) != len(tt.wantNames) {
				t.Fatalf("got %d steps %v, want %d %v", len(names), names, len(tt.wantNames), tt.wantNames)
			}
			for i, want := range tt.wantNames {
				if names[i] != want {
					t.Errorf("step[%d] = %q, want %q", i, names[i], want)
				}
			}
		})
	}
}

func TestStepScripts(t *testing.T) {
	t.Run("egress step references setup and enable scripts", func(t *testing.T) {
		steps := Steps("agent", false)
		if len(steps) != 1 {
			t.Fatalf("expected 1 step, got %d", len(steps))
		}
		if steps[0].Script != "/usr/local/bin/pixels-setup-egress.sh" {
			t.Errorf("egress script = %q, want pixels-setup-egress.sh", steps[0].Script)
		}
		if steps[0].Finalize != "/usr/local/bin/pixels-enable-egress.sh" {
			t.Errorf("egress finalize = %q, want pixels-enable-egress.sh", steps[0].Finalize)
		}
	})

	t.Run("devtools step runs setup script", func(t *testing.T) {
		steps := Steps("unrestricted", true)
		if len(steps) != 1 {
			t.Fatalf("expected 1 step, got %d", len(steps))
		}
		if !contains(steps[0].Script, "pixels-setup-devtools.sh") {
			t.Error("devtools script missing setup command")
		}
	})
}

func TestStepNames(t *testing.T) {
	steps := []Step{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
	}
	names := StepNames(steps)
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("StepNames = %v, want [a b c]", names)
	}
}

func TestParseSessions(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if s := ParseSessions(""); s != nil {
			t.Errorf("expected nil, got %v", s)
		}
	})

	t.Run("completed session", func(t *testing.T) {
		raw := "session_name=px-egress\tpid=1234\ttask_ended_at=100\ttask_exit_code=0\tcmd=bash"
		sessions := ParseSessions(raw)
		if len(sessions) != 1 || sessions[0].Name != "px-egress" || sessions[0].EndedAt != "100" || sessions[0].ExitCode != "0" {
			t.Errorf("unexpected: %+v", sessions)
		}
	})

	t.Run("skips non-session lines", func(t *testing.T) {
		raw := "session_name=px-devtools\tpid=1\n" +
			"garbage line\n" +
			"session_name=px-egress\tpid=2"
		sessions := ParseSessions(raw)
		if len(sessions) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(sessions))
		}
	})
}

func TestScript(t *testing.T) {
	t.Run("single step", func(t *testing.T) {
		steps := []Step{{Name: "px-devtools", Script: "/usr/local/bin/pixels-setup-devtools.sh"}}
		script := Script(steps)
		for _, want := range []string{
			"#!/bin/sh",
			zmxVersion,
			"zmx --version",
			".pixels-provisioned",
			".ssh-provisioned",
			"zmx run px-devtools",
			"zmx wait px-devtools",
		} {
			if !strings.Contains(script, want) {
				t.Errorf("script missing %q", want)
			}
		}
	})

	t.Run("concurrent steps with deferred egress", func(t *testing.T) {
		steps := Steps("agent", true)
		script := Script(steps)
		// Both zmx run commands should appear before the zmx wait.
		runDev := strings.Index(script, "zmx run px-devtools")
		runEgress := strings.Index(script, "zmx run px-egress")
		waitAll := strings.Index(script, "zmx wait px-devtools px-egress")
		if runDev < 0 || runEgress < 0 || waitAll < 0 {
			t.Fatal("missing step commands")
		}
		if runDev > waitAll || runEgress > waitAll {
			t.Error("all zmx run calls should precede zmx wait")
		}
		// Finalize (egress lockdown) should appear after the wait.
		enable := strings.Index(script, "pixels-enable-egress.sh")
		if enable < 0 || enable < waitAll {
			t.Error("egress finalize should run after zmx wait")
		}
	})

	t.Run("idempotency guard before zmx", func(t *testing.T) {
		script := Script(Steps("agent", true))
		sentinel := strings.Index(script, "SENTINEL")
		zmx := strings.Index(script, "zmx")
		if sentinel < 0 || zmx < 0 || sentinel > zmx {
			t.Error("sentinel check should precede zmx commands")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && // avoid trivial matches
		(len(s) >= len(substr)) &&
		(s == substr || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
