package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquirePIDFileSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pf.Release()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	if got, want := string(b), fmt.Sprintf("%d\n", os.Getpid()); got != want {
		t.Errorf("pidfile content = %q, want %q", got, want)
	}
}

func TestAcquirePIDFileLiveCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := AcquirePIDFile(path)
	if err == nil {
		t.Fatal("expected collision error")
	}
}

func TestAcquirePIDFileStalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("acquire: %v (expected stale PID to be overwritten)", err)
	}
	defer pf.Release()
}

func TestPIDFileReleaseRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pf.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pidfile should be removed after Release")
	}
}
