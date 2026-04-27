package truenas

import (
	"errors"
	"testing"

	"github.com/deevus/pixels/sandbox"
)

func TestWrapNotFoundDetectsLowercase(t *testing.T) {
	err := wrapNotFound(errors.New("instance not found"))
	if !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("expected errors.Is to match sandbox.ErrNotFound; got %v", err)
	}
}

func TestWrapNotFoundDetectsCapitalised(t *testing.T) {
	err := wrapNotFound(errors.New("404 Not Found"))
	if !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("expected errors.Is to match sandbox.ErrNotFound; got %v", err)
	}
}

func TestWrapNotFoundPassesUnrelatedThrough(t *testing.T) {
	original := errors.New("permission denied")
	err := wrapNotFound(original)
	if errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("permission-denied should not match sandbox.ErrNotFound")
	}
	if err.Error() != original.Error() {
		t.Errorf("expected original error returned; got %v", err)
	}
}

func TestWrapNotFoundIdempotent(t *testing.T) {
	once := wrapNotFound(errors.New("not found"))
	twice := wrapNotFound(once)
	if !errors.Is(twice, sandbox.ErrNotFound) {
		t.Errorf("double-wrap should still match")
	}
	if once.Error() != twice.Error() {
		t.Errorf("double-wrap should not change the message; got %q -> %q", once.Error(), twice.Error())
	}
}

func TestWrapNotFoundNilPasses(t *testing.T) {
	if err := wrapNotFound(nil); err != nil {
		t.Errorf("expected nil; got %v", err)
	}
}
