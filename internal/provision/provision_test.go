package provision

import (
	"context"
	"errors"
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

func TestNewRunner(t *testing.T) {
	r := NewRunner("10.0.0.1", "root", "/tmp/key")
	if r.Host != "10.0.0.1" {
		t.Errorf("Host = %q, want %q", r.Host, "10.0.0.1")
	}
	if r.User != "root" {
		t.Errorf("User = %q, want %q", r.User, "root")
	}
	if r.KeyPath != "/tmp/key" {
		t.Errorf("KeyPath = %q, want %q", r.KeyPath, "/tmp/key")
	}
	if r.exec == nil {
		t.Fatal("exec should not be nil")
	}
}

func TestInstallZmx(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		err     error
		wantErr string
	}{
		{"success", 0, nil, ""},
		{"ssh error", 0, errors.New("connection refused"), "installing zmx:"},
		{"non-zero exit", 5, nil, "installing zmx: exit code 5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured []string
			r := NewRunnerWith(&MockExecutor{
				ExecFunc: func(ctx context.Context, command []string) (int, error) {
					captured = command
					return tt.code, tt.err
				},
			})
			err := r.InstallZmx(context.Background())
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(captured) == 0 || !strings.Contains(captured[0], zmxVersion) {
					t.Errorf("command should contain zmx version, got %v", captured)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		err     error
		wantErr string
	}{
		{"success", 0, nil, ""},
		{"ssh error", 0, errors.New("connection refused"), "starting px-test:"},
		{"non-zero exit", 1, nil, "starting px-test: exit code 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured []string
			r := NewRunnerWith(&MockExecutor{
				ExecFunc: func(ctx context.Context, command []string) (int, error) {
					captured = command
					return tt.code, tt.err
				},
			})
			step := Step{Name: "px-test", Script: "/usr/local/bin/test.sh"}
			err := r.Run(context.Background(), step)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				cmd := captured[0]
				if !strings.Contains(cmd, "zmx run px-test /usr/local/bin/test.sh") {
					t.Errorf("command missing zmx run, got %q", cmd)
				}
				if !strings.Contains(cmd, ">/dev/null 2>&1") {
					t.Errorf("command missing redirect, got %q", cmd)
				}
				if !strings.HasPrefix(cmd, "unset XDG_RUNTIME_DIR") {
					t.Errorf("command missing XDG unset, got %q", cmd)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestWait(t *testing.T) {
	tests := []struct {
		name    string
		names   []string
		code    int
		err     error
		wantErr string
	}{
		{"single name", []string{"px-devtools"}, 0, nil, ""},
		{"multiple names", []string{"px-devtools", "px-egress"}, 0, nil, ""},
		{"ssh error", []string{"px-test"}, 0, errors.New("timeout"), "waiting for steps:"},
		{"non-zero exit", []string{"px-test"}, 1, nil, "one or more provisioning steps failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured []string
			r := NewRunnerWith(&MockExecutor{
				ExecFunc: func(ctx context.Context, command []string) (int, error) {
					captured = command
					return tt.code, tt.err
				},
			})
			err := r.Wait(context.Background(), tt.names...)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				cmd := captured[0]
				for _, n := range tt.names {
					if !strings.Contains(cmd, n) {
						t.Errorf("command missing name %q, got %q", n, cmd)
					}
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestList(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := NewRunnerWith(&MockExecutor{
			OutputFunc: func(ctx context.Context, command []string) ([]byte, error) {
				return []byte("  session_name=px-test\tpid=1  \n"), nil
			},
		})
		out, err := r.List(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "session_name=px-test\tpid=1" {
			t.Errorf("output = %q, want trimmed", out)
		}
	})

	t.Run("error", func(t *testing.T) {
		r := NewRunnerWith(&MockExecutor{
			OutputFunc: func(ctx context.Context, command []string) ([]byte, error) {
				return nil, errors.New("connection refused")
			},
		})
		_, err := r.List(context.Background())
		if err == nil || !strings.Contains(err.Error(), "listing zmx sessions") {
			t.Errorf("error = %v, want containing 'listing zmx sessions'", err)
		}
	})
}

func TestHistory(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := NewRunnerWith(&MockExecutor{
			OutputFunc: func(ctx context.Context, command []string) ([]byte, error) {
				if !strings.Contains(command[0], "zmx history px-test") {
					t.Errorf("command missing history, got %v", command)
				}
				return []byte("line1\nline2\n"), nil
			},
		})
		out, err := r.History(context.Background(), "px-test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "line1\nline2\n" {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("error", func(t *testing.T) {
		r := NewRunnerWith(&MockExecutor{
			OutputFunc: func(ctx context.Context, command []string) ([]byte, error) {
				return nil, errors.New("not found")
			},
		})
		_, err := r.History(context.Background(), "px-test")
		if err == nil || !strings.Contains(err.Error(), "getting history for px-test") {
			t.Errorf("error = %v, want containing 'getting history'", err)
		}
	})
}

func TestIsProvisioned(t *testing.T) {
	tests := []struct {
		name string
		code int
		err  error
		want bool
	}{
		{"file exists", 0, nil, true},
		{"file missing", 1, nil, false},
		{"ssh error", 0, errors.New("timeout"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunnerWith(&MockExecutor{
				ExecFunc: func(ctx context.Context, command []string) (int, error) {
					if !strings.Contains(command[0], ".pixels-provisioned") {
						t.Errorf("command should check sentinel, got %v", command)
					}
					return tt.code, tt.err
				},
			})
			if got := r.IsProvisioned(context.Background()); got != tt.want {
				t.Errorf("IsProvisioned() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasProvisionScript(t *testing.T) {
	tests := []struct {
		name string
		code int
		err  error
		want bool
	}{
		{"script exists", 0, nil, true},
		{"script missing", 1, nil, false},
		{"ssh error", 0, errors.New("timeout"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunnerWith(&MockExecutor{
				ExecFunc: func(ctx context.Context, command []string) (int, error) {
					if !strings.Contains(command[0], "pixels-provision.sh") {
						t.Errorf("command should check provision script, got %v", command)
					}
					return tt.code, tt.err
				},
			})
			if got := r.HasProvisionScript(context.Background()); got != tt.want {
				t.Errorf("HasProvisionScript() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPollStatus(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		outErr   error
		names    []string
		wantStr  string
		wantDone bool
	}{
		{
			name:     "all done",
			output:   "session_name=px-devtools\ttask_ended_at=100\ttask_exit_code=0",
			names:    []string{"px-devtools"},
			wantStr:  "px-devtools done",
			wantDone: true,
		},
		{
			name:     "still running",
			output:   "session_name=px-devtools\tpid=1",
			names:    []string{"px-devtools"},
			wantStr:  "px-devtools running",
			wantDone: false,
		},
		{
			name:     "step pending (not in list)",
			output:   "",
			names:    []string{"px-devtools"},
			wantStr:  "px-devtools pending",
			wantDone: false,
		},
		{
			name:     "step failed",
			output:   "session_name=px-devtools\ttask_ended_at=100\ttask_exit_code=1",
			names:    []string{"px-devtools"},
			wantStr:  "px-devtools failed (exit 1)",
			wantDone: true,
		},
		{
			name:     "list error",
			outErr:   errors.New("connection refused"),
			names:    []string{"px-devtools"},
			wantStr:  "",
			wantDone: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunnerWith(&MockExecutor{
				OutputFunc: func(ctx context.Context, command []string) ([]byte, error) {
					return []byte(tt.output), tt.outErr
				},
			})
			status, done := r.PollStatus(context.Background(), tt.names)
			if status != tt.wantStr {
				t.Errorf("status = %q, want %q", status, tt.wantStr)
			}
			if done != tt.wantDone {
				t.Errorf("done = %v, want %v", done, tt.wantDone)
			}
		})
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
