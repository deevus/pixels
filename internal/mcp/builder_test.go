package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuilderDedupesConcurrentCalls(t *testing.T) {
	var doBuildCalls atomic.Int32
	b := &Builder{
		DoBuild: func(ctx context.Context, name string) error {
			doBuildCalls.Add(1)
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Build(context.Background(), "alpha")
		}()
	}
	wg.Wait()

	if got := doBuildCalls.Load(); got != 1 {
		t.Errorf("DoBuild called %d times; want exactly 1 (deduplicated)", got)
	}
}

func TestBuilderCachesFailures(t *testing.T) {
	var doBuildCalls atomic.Int32
	b := &Builder{
		FailureTTL: 1 * time.Hour,
		DoBuild: func(ctx context.Context, name string) error {
			doBuildCalls.Add(1)
			return errors.New("build failed")
		},
	}

	if err := b.Build(context.Background(), "alpha"); err == nil {
		t.Fatal("first call should have errored")
	}
	if err := b.Build(context.Background(), "alpha"); err == nil {
		t.Fatal("cached call should have errored")
	}
	if got := doBuildCalls.Load(); got != 1 {
		t.Errorf("DoBuild called %d times; want exactly 1 (second hit cache)", got)
	}
}

func TestBuilderStatusReportsBuilding(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	b := &Builder{
		DoBuild: func(ctx context.Context, name string) error {
			close(started)
			<-finish
			return nil
		},
	}

	go func() { _ = b.Build(context.Background(), "alpha") }()
	<-started
	if status, _ := b.Status("alpha"); status != "building" {
		t.Errorf("Status = %q, want building", status)
	}

	close(finish)
	deadline := time.After(2 * time.Second)
	for {
		if status, _ := b.Status("alpha"); status == "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Status never cleared")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestBuilderStatusReportsFailed(t *testing.T) {
	b := &Builder{
		FailureTTL: 1 * time.Hour,
		DoBuild: func(ctx context.Context, name string) error {
			return errors.New("nope")
		},
	}
	_ = b.Build(context.Background(), "alpha")
	status, err := b.Status("alpha")
	if status != "failed" {
		t.Errorf("Status = %q, want failed", status)
	}
	if err == nil || err.Error() != "nope" {
		t.Errorf("err = %v, want nope", err)
	}
}
