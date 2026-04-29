package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// TestAcquirePIDFileLiveCollision asserts that a second acquire fails while
// the first holder still holds the flock. The error must surface the
// holder's PID so operators can identify the running process.
func TestAcquirePIDFileLiveCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer pf.Release()

	_, err = AcquirePIDFile(path)
	if err == nil {
		t.Fatal("expected collision error while lock is held")
	}
	want := strconv.Itoa(os.Getpid())
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain holder PID %s", err.Error(), want)
	}
}

// TestAcquirePIDFileStalePID verifies that a pidfile left behind by a
// crashed prior holder (no flock held) does not block a new acquire.
func TestAcquirePIDFileStalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("acquire: %v (stale pidfile should not block)", err)
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
		pf.Release()
	}
}

// TestPIDFileReleaseFreesLock asserts that Release drops the flock so a
// fresh acquire succeeds. The pidfile itself is intentionally left on disk;
// see PIDFile.Release for the rationale.
func TestPIDFileReleaseFreesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pf.Release()

	pf2, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("re-acquire after Release: %v", err)
	}
	pf2.Release()
}
