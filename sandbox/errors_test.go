package sandbox

import (
	"errors"
	"testing"
)

func TestWrapNotFoundDetectsLowercase(t *testing.T) {
	err := WrapNotFound(errors.New("instance not found"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected errors.Is to match ErrNotFound; got %v", err)
	}
}

func TestWrapNotFoundDetectsCapitalised(t *testing.T) {
	err := WrapNotFound(errors.New("404 Not Found"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected errors.Is to match ErrNotFound; got %v", err)
	}
}

func TestWrapNotFoundPassesUnrelatedThrough(t *testing.T) {
	original := errors.New("permission denied")
	err := WrapNotFound(original)
	if errors.Is(err, ErrNotFound) {
		t.Errorf("permission-denied should not match ErrNotFound")
	}
	if err.Error() != original.Error() {
		t.Errorf("expected original error returned; got %v", err)
	}
}

func TestWrapNotFoundIdempotent(t *testing.T) {
	once := WrapNotFound(errors.New("not found"))
	twice := WrapNotFound(once)
	if !errors.Is(twice, ErrNotFound) {
		t.Errorf("double-wrap should still match")
	}
	if once.Error() != twice.Error() {
		t.Errorf("double-wrap should not change the message; got %q -> %q", once.Error(), twice.Error())
	}
}

func TestWrapNotFoundDoubleWrapUnrelatedNoMatch(t *testing.T) {
	original := errors.New("permission denied")
	once := WrapNotFound(original)
	twice := WrapNotFound(once)
	if errors.Is(twice, ErrNotFound) {
		t.Errorf("double-wrap of unrelated error should not match ErrNotFound")
	}
	if twice.Error() != original.Error() {
		t.Errorf("double-wrap should preserve original message; got %v", twice)
	}
}

func TestWrapNotFoundNilPasses(t *testing.T) {
	if err := WrapNotFound(nil); err != nil {
		t.Errorf("expected nil; got %v", err)
	}
}
