package sandbox

import (
	"testing"
)

func TestOpenUnknownBackend(t *testing.T) {
	_, err := Open("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unregistered backend, got nil")
	}
	want := `sandbox: unknown backend "nonexistent"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRegisterAndOpen(t *testing.T) {
	// Clean up after test to avoid polluting global state.
	defer delete(backends, "test")

	Register("test", func(cfg map[string]string) (Sandbox, error) {
		return nil, nil
	})

	_, err := Open("test", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterOverwrites(t *testing.T) {
	defer delete(backends, "dup")

	called := ""
	Register("dup", func(cfg map[string]string) (Sandbox, error) {
		called = "first"
		return nil, nil
	})
	Register("dup", func(cfg map[string]string) (Sandbox, error) {
		called = "second"
		return nil, nil
	})

	_, _ = Open("dup", nil)
	if called != "second" {
		t.Errorf("expected second factory to win, got %q", called)
	}
}
