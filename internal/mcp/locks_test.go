package mcp

import (
	"sync"
	"testing"
)

func TestSandboxLocksReturnsSameMutexForName(t *testing.T) {
	l := &SandboxLocks{}
	a := l.For("alpha")
	b := l.For("alpha")
	if a != b {
		t.Errorf("expected same *Mutex pointer for the same name; got %p and %p", a, b)
	}
}

func TestSandboxLocksReturnsDistinctMutexForDifferentNames(t *testing.T) {
	l := &SandboxLocks{}
	a := l.For("alpha")
	b := l.For("beta")
	if a == b {
		t.Errorf("expected distinct mutexes for different names")
	}
}

func TestSandboxLocksConcurrentAccessNoRace(t *testing.T) {
	l := &SandboxLocks{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := l.For("sb")
			m.Lock()
			m.Unlock()
		}(i)
	}
	wg.Wait()
}
