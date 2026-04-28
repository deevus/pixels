package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// TestAcquirePIDFileConcurrent races N goroutines on the same path. Exactly
// one should win; the rest must observe the live owner and fail.
func TestAcquirePIDFileConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	const N = 32

	var wg sync.WaitGroup
	var winners atomic.Int32
	var failures atomic.Int32
	pfCh := make(chan *PIDFile, N)
	start := make(chan struct{})

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pf, err := AcquirePIDFile(path)
			if err == nil {
				winners.Add(1)
				pfCh <- pf
				return
			}
			failures.Add(1)
		}()
	}
	close(start)
	wg.Wait()
	close(pfCh)

	if got := winners.Load(); got != 1 {
		t.Errorf("winners = %d, want exactly 1", got)
	}
	if got := failures.Load(); got != N-1 {
		t.Errorf("failures = %d, want %d", got, N-1)
	}
	for pf := range pfCh {
		_ = pf.Release()
	}
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
