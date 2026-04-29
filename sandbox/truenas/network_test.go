package truenas

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/ssh"
	"github.com/deevus/pixels/sandbox"
)

// mockSSH records SSH calls for test verification.
type mockSSH struct {
	execCalls     []mockSSHCall
	outputCalls   []mockSSHCall
	waitCalls     []string
	testAuthCalls []mockSSHCall

	// Configurable responses.
	execFn     func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error)
	outputFn   func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error)
	waitFn     func(ctx context.Context, host string, timeout time.Duration, log io.Writer) error
	testAuthFn func(ctx context.Context, cc ssh.ConnConfig) error
}

type mockSSHCall struct {
	Host string
	User string
	Cmd  []string
}

func (m *mockSSH) Exec(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
	m.execCalls = append(m.execCalls, mockSSHCall{Host: cc.Host, User: cc.User, Cmd: cmd})
	if m.execFn != nil {
		return m.execFn(ctx, cc, cmd)
	}
	return 0, nil
}

func (m *mockSSH) ExecQuiet(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
	m.execCalls = append(m.execCalls, mockSSHCall{Host: cc.Host, User: cc.User, Cmd: cmd})
	if m.execFn != nil {
		return m.execFn(ctx, cc, cmd)
	}
	return 0, nil
}

func (m *mockSSH) OutputQuiet(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
	m.outputCalls = append(m.outputCalls, mockSSHCall{Host: cc.Host, User: cc.User, Cmd: cmd})
	if m.outputFn != nil {
		return m.outputFn(ctx, cc, cmd)
	}
	return nil, nil
}

// probeOK returns an outputFn that satisfies Ready()'s smoke probe by
// returning a non-empty `id`-style line for any call.
func probeOK() func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
	return func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
		return []byte("uid=1000(pixel) gid=1000(pixel)\n"), nil
	}
}

func (m *mockSSH) WaitReady(ctx context.Context, host string, timeout time.Duration, log io.Writer) error {
	m.waitCalls = append(m.waitCalls, host)
	if m.waitFn != nil {
		return m.waitFn(ctx, host, timeout, log)
	}
	return nil
}

func (m *mockSSH) TestAuth(ctx context.Context, cc ssh.ConnConfig) error {
	m.testAuthCalls = append(m.testAuthCalls, mockSSHCall{Host: cc.Host, User: cc.User})
	if m.testAuthFn != nil {
		return m.testAuthFn(ctx, cc)
	}
	return nil
}

// writeCall records a WriteContainerFile call.
type writeCall struct {
	name    string
	path    string
	content string
	mode    fs.FileMode
}

// runningInstanceFunc returns a GetInstanceFunc that returns a running instance at the given IP.
func runningInstanceFunc(ip string) func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
	return func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
		return &tnapi.VirtInstance{
			Name:    name,
			Status:  "RUNNING",
			Aliases: []tnapi.VirtAlias{{Type: "INET", Address: ip}},
		}, nil
	}
}

func TestSetEgressModeUnrestricted(t *testing.T) {
	var writes []writeCall
	mssh := &mockSSH{}

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
			},
		},
		Filesystem: &tnapi.MockFilesystemService{
			WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
				writes = append(writes, writeCall{
					path:    path,
					content: string(params.Content),
					mode:    params.Mode,
				})
				return nil
			},
		},
	}, mssh, testCfg())

	if err := tn.SetEgressMode(context.Background(), "test", sandbox.EgressUnrestricted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have flushed nftables and removed files via SSH.
	if len(mssh.execCalls) < 2 {
		t.Fatalf("expected >= 2 SSH exec calls, got %d", len(mssh.execCalls))
	}

	// First call: flush nftables.
	if !strings.Contains(strings.Join(mssh.execCalls[0].Cmd, " "), "nft flush") {
		t.Errorf("first SSH call should flush nftables, got %v", mssh.execCalls[0].Cmd)
	}

	// Second call: rm egress files.
	if !strings.Contains(strings.Join(mssh.execCalls[1].Cmd, " "), "rm -f") {
		t.Errorf("second SSH call should remove files, got %v", mssh.execCalls[1].Cmd)
	}

	// Should write unrestricted sudoers via API.
	if len(writes) == 0 {
		t.Fatal("expected write calls")
	}
	found := false
	for _, w := range writes {
		if strings.Contains(w.path, "sudoers") && strings.Contains(w.content, "NOPASSWD: ALL") {
			found = true
		}
	}
	if !found {
		t.Error("should write unrestricted sudoers")
	}
}

func TestSetEgressModeAllowlist(t *testing.T) {
	var writes []writeCall
	mssh := &mockSSH{}

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
			},
		},
		Filesystem: &tnapi.MockFilesystemService{
			WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
				writes = append(writes, writeCall{
					path:    path,
					content: string(params.Content),
					mode:    params.Mode,
				})
				return nil
			},
		},
	}, mssh, testCfg())

	if err := tn.SetEgressMode(context.Background(), "test", sandbox.EgressAllowlist); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify files written via API.
	writePaths := make(map[string]bool)
	for _, w := range writes {
		// Extract relative path from full rootfs path.
		writePaths[w.path] = true
	}

	// Should write nftables, resolve script, safe-apt, sudoers.
	wantPaths := []string{"nftables.conf", "pixels-resolve-egress.sh", "safe-apt", "sudoers"}
	for _, want := range wantPaths {
		found := false
		for p := range writePaths {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing write for %q", want)
		}
	}

	// Should install nftables and resolve via SSH.
	var hasInstall, hasResolve bool
	for _, call := range mssh.execCalls {
		cmd := strings.Join(call.Cmd, " ")
		if strings.Contains(cmd, "apt-get install") && strings.Contains(cmd, "nftables") {
			hasInstall = true
		}
		if strings.Contains(cmd, "pixels-resolve-egress.sh") {
			hasResolve = true
		}
	}
	if !hasInstall {
		t.Error("should install nftables")
	}
	if !hasResolve {
		t.Error("should run resolve script")
	}
}

func TestAllowDomain(t *testing.T) {
	var lastWritten string
	mssh := &mockSSH{
		execFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
			return 0, nil
		},
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			return []byte("existing.com\n"), nil
		},
	}

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
			},
		},
		Filesystem: &tnapi.MockFilesystemService{
			WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
				lastWritten = string(params.Content)
				return nil
			},
		},
	}, mssh, testCfg())

	if err := tn.AllowDomain(context.Background(), "test", "new.example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(lastWritten, "existing.com") {
		t.Error("should preserve existing domains")
	}
	if !strings.Contains(lastWritten, "new.example.com") {
		t.Error("should append new domain")
	}
}

func TestAllowDomainDuplicate(t *testing.T) {
	mssh := &mockSSH{
		execFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
			return 0, nil
		},
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			return []byte("example.com\n"), nil
		},
	}

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
			},
		},
		Filesystem: &tnapi.MockFilesystemService{
			WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
				t.Error("should not write when domain already exists")
				return nil
			},
		},
	}, mssh, testCfg())

	if err := tn.AllowDomain(context.Background(), "test", "example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDenyDomain(t *testing.T) {
	var lastWritten string
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			return []byte("keep.com\nremove.com\nalso-keep.com\n"), nil
		},
	}

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
			},
		},
		Filesystem: &tnapi.MockFilesystemService{
			WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
				lastWritten = string(params.Content)
				return nil
			},
		},
	}, mssh, testCfg())

	if err := tn.DenyDomain(context.Background(), "test", "remove.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(lastWritten, "remove.com") {
		t.Error("should remove domain")
	}
	if !strings.Contains(lastWritten, "keep.com") {
		t.Error("should keep other domains")
	}
	if !strings.Contains(lastWritten, "also-keep.com") {
		t.Error("should keep other domains")
	}
}

func TestDenyDomainNotFound(t *testing.T) {
	mssh := &mockSSH{
		outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
			return []byte("other.com\n"), nil
		},
	}

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
			},
		},
		Filesystem: &tnapi.MockFilesystemService{},
	}, mssh, testCfg())

	err := tn.DenyDomain(context.Background(), "test", "missing.com")
	if err == nil {
		t.Fatal("expected error for missing domain")
	}
	if !strings.Contains(err.Error(), "not in allowlist") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestGetPolicy(t *testing.T) {
	t.Run("unrestricted", func(t *testing.T) {
		mssh := &mockSSH{
			execFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
				// test -f returns 1 (file not found).
				return 1, nil
			},
		}

		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			},
		}, mssh, testCfg())

		policy, err := tn.GetPolicy(context.Background(), "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if policy.Mode != sandbox.EgressUnrestricted {
			t.Errorf("mode = %q, want unrestricted", policy.Mode)
		}
	})

	t.Run("restricted", func(t *testing.T) {
		mssh := &mockSSH{
			execFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
				return 0, nil // file exists
			},
			outputFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
				return []byte("api.example.com\ncdn.example.com\n"), nil
			},
		}

		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			},
		}, mssh, testCfg())

		policy, err := tn.GetPolicy(context.Background(), "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if policy.Mode != sandbox.EgressAllowlist {
			t.Errorf("mode = %q, want allowlist", policy.Mode)
		}
		if len(policy.Domains) != 2 {
			t.Fatalf("domains = %v", policy.Domains)
		}
		if policy.Domains[0] != "api.example.com" {
			t.Errorf("domains[0] = %q", policy.Domains[0])
		}
	})
}

func TestParseDomains(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"normal", "a.com\nb.com\n", []string{"a.com", "b.com"}},
		{"trailing whitespace", "  a.com  \n  b.com  \n", []string{"a.com", "b.com"}},
		{"empty lines", "a.com\n\nb.com\n\n", []string{"a.com", "b.com"}},
		{"empty string", "", nil},
		{"single domain", "example.com\n", []string{"example.com"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDomains(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
