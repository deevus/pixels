package mcp

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBuildLockSerialisesSameName(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireBuildLock(dir, "alpha")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire should block until first is released.
	got := make(chan error, 1)
	go func() {
		_, err := AcquireBuildLock(dir, "alpha")
		got <- err
	}()

	select {
	case <-got:
		t.Fatal("second acquire returned before first was released")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}

	first.Release()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("second acquire after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never returned after release")
	}
}

func TestBuildLockDifferentNamesIndependent(t *testing.T) {
	dir := t.TempDir()
	a, err := AcquireBuildLock(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()

	b, err := AcquireBuildLock(dir, "beta")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()

	// If we got here without blocking, the locks are per-name — correct.
	_ = filepath.Join(dir, "irrelevant")
}
