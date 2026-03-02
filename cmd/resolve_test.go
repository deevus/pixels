package cmd

import (
	"testing"
)

func TestValidSessionName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"console", "console", true},
		{"build", "build", true},
		{"my-session", "my-session", true},
		{"test.1", "test.1", true},
		{"a_b", "a_b", true},
		{"empty", "", false},
		{"has space", "has space", false},
		{"semicolon", "semi;colon", false},
		{"backtick", "back`tick", false},
		{"newline", "new\nline", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validSessionName.MatchString(tt.input); got != tt.want {
				t.Errorf("validSessionName.MatchString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainerName(t *testing.T) {
	if got := containerName("my-project"); got != "px-my-project" {
		t.Errorf("containerName(my-project) = %q, want %q", got, "px-my-project")
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"px-my-project", "my-project"},
		{"px-sandbox", "sandbox"},
		{"no-prefix", "no-prefix"},
	}
	for _, tt := range tests {
		if got := displayName(tt.input); got != tt.want {
			t.Errorf("displayName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
