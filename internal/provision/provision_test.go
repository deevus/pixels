package provision

import (
	"testing"
)

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
	t.Run("egress step installs nftables and activates restricted sudoers", func(t *testing.T) {
		steps := Steps("agent", false)
		if len(steps) != 1 {
			t.Fatalf("expected 1 step, got %d", len(steps))
		}
		s := steps[0].Script
		for _, want := range []string{
			"nftables",
			"dnsutils",
			"pixels-resolve-egress.sh",
			"pixel.restricted",
			"sudoers.d/pixel",
		} {
			if !contains(s, want) {
				t.Errorf("egress script missing %q", want)
			}
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
