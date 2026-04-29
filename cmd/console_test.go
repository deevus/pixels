package cmd

import (
	"strings"
	"testing"
)

func TestZmxAttachCmdEscapesSession(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		contains string
	}{
		{"plain", "console", "zmx attach console bash -l"},
		{"with-dash", "my-session", "zmx attach my-session bash -l"},
		{"with-dot", "test.1", "zmx attach test.1 bash -l"},
		{"semicolon", "foo;rm -rf /", "zmx attach 'foo;rm -rf /' bash -l"},
		{"backtick", "foo`id`", "zmx attach 'foo`id`' bash -l"},
		{"dollar", "foo$(id)", "zmx attach 'foo$(id)' bash -l"},
		{"single-quote", "foo'bar", `zmx attach 'foo'"'"'bar' bash -l`},
		{"space", "foo bar", "zmx attach 'foo bar' bash -l"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := zmxAttachCmd(tt.session)
			if len(got) != 3 {
				t.Fatalf("zmxAttachCmd returned %d args, want 3", len(got))
			}
			if got[0] != "sh" || got[1] != "-lc" {
				t.Errorf("zmxAttachCmd prefix = %q %q, want sh -lc", got[0], got[1])
			}
			if !strings.Contains(got[2], tt.contains) {
				t.Errorf("zmxAttachCmd(%q)[2] = %q, want substring %q", tt.session, got[2], tt.contains)
			}
		})
	}
}
