package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirXDG(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", d)

	got := dir()
	want := filepath.Join(d, "pixels")
	if got != want {
		t.Errorf("dir() = %q, want %q", got, want)
	}
}

func TestDirDefault(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")

	got := dir()
	cacheDir, _ := os.UserCacheDir()
	want := filepath.Join(cacheDir, "pixels")
	if got != want {
		t.Errorf("dir() = %q, want %q", got, want)
	}
}

func TestPutGetDelete(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	e := &Entry{IP: "10.0.0.5", Status: "RUNNING"}
	Put("test-pixel", e)

	got := Get("test-pixel")
	if got == nil {
		t.Fatal("Get() returned nil after Put()")
	}
	if got.IP != "10.0.0.5" {
		t.Errorf("IP = %q, want %q", got.IP, "10.0.0.5")
	}
	if got.Status != "RUNNING" {
		t.Errorf("Status = %q, want %q", got.Status, "RUNNING")
	}

	Delete("test-pixel")
	if Get("test-pixel") != nil {
		t.Error("Get() returned non-nil after Delete()")
	}
}

func TestGetMissing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	if Get("nonexistent") != nil {
		t.Error("Get() should return nil for missing entry")
	}
}
