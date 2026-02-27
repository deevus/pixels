package egress

import (
	"strings"
	"testing"
)

func TestPresetDomains(t *testing.T) {
	domains := PresetDomains("agent")
	if len(domains) == 0 {
		t.Fatal("PresetDomains(\"agent\") returned empty")
	}

	// Spot-check critical domains.
	required := []string{
		"api.anthropic.com",
		"api.openai.com",
		"registry.npmjs.org",
		"github.com",
		"pypi.org",
	}
	domainSet := make(map[string]bool)
	for _, d := range domains {
		domainSet[d] = true
	}
	for _, r := range required {
		if !domainSet[r] {
			t.Errorf("PresetDomains(\"agent\") missing %q", r)
		}
	}
}

func TestPresetDomainsUnknown(t *testing.T) {
	if got := PresetDomains("nonexistent"); got != nil {
		t.Errorf("PresetDomains(\"nonexistent\") = %v, want nil", got)
	}
}

func TestResolvedDomains(t *testing.T) {
	tests := []struct {
		name   string
		egress string
		allow  []string
		want   int // minimum expected domains
	}{
		{"unrestricted returns nil", "unrestricted", nil, 0},
		{"agent returns preset", "agent", nil, 10},
		{"agent merges allow", "agent", []string{"custom.example.com"}, 11},
		{"allowlist returns only allow", "allowlist", []string{"a.com", "b.com"}, 2},
		{"allowlist empty returns empty", "allowlist", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDomains(tt.egress, tt.allow)
			if tt.egress == "unrestricted" {
				if got != nil {
					t.Errorf("got %v, want nil for unrestricted", got)
				}
				return
			}
			if len(got) < tt.want {
				t.Errorf("got %d domains, want >= %d", len(got), tt.want)
			}
			// Check no duplicates.
			seen := make(map[string]bool)
			for _, d := range got {
				if seen[d] {
					t.Errorf("duplicate domain: %s", d)
				}
				seen[d] = true
			}
		})
	}
}

func TestNftablesConf(t *testing.T) {
	conf := NftablesConf()
	if !strings.Contains(conf, "table inet pixels_egress") {
		t.Error("missing table definition")
	}
	if !strings.Contains(conf, "policy drop") {
		t.Error("missing drop policy")
	}
	if !strings.Contains(conf, "@allowed_v4") {
		t.Error("missing allowed_v4 set reference")
	}
	if !strings.Contains(conf, "oif lo accept") {
		t.Error("missing loopback rule")
	}
}

func TestResolveScript(t *testing.T) {
	script := ResolveScript()
	if !strings.Contains(script, "#!/bin/bash") {
		t.Error("missing shebang")
	}
	if !strings.Contains(script, "pixels-egress-domains") {
		t.Error("missing domain file reference")
	}
	if !strings.Contains(script, "nft") {
		t.Error("missing nft command")
	}
}

func TestDomainsFileContent(t *testing.T) {
	domains := []string{"api.anthropic.com", "github.com"}
	content := DomainsFileContent(domains)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 2 {
		t.Errorf("got %d lines, want 2", len(lines))
	}
	if lines[0] != "api.anthropic.com" {
		t.Errorf("line[0] = %q, want %q", lines[0], "api.anthropic.com")
	}
}

func TestSudoersRestricted(t *testing.T) {
	s := SudoersRestricted()
	if strings.Contains(s, "/usr/bin/apt-get") {
		t.Error("should not allow apt-get directly (use safe-apt wrapper)")
	}
	if !strings.Contains(s, "/usr/local/bin/safe-apt") {
		t.Error("missing safe-apt wrapper allowlist")
	}
	if !strings.Contains(s, "/usr/bin/journalctl") {
		t.Error("missing journalctl allowlist")
	}
	if strings.Contains(s, "ALL=(ALL) NOPASSWD: ALL") {
		t.Error("should not contain blanket NOPASSWD:ALL")
	}
}

func TestSudoersUnrestricted(t *testing.T) {
	s := SudoersUnrestricted()
	if !strings.Contains(s, "NOPASSWD: ALL") {
		t.Error("missing blanket NOPASSWD:ALL")
	}
}
