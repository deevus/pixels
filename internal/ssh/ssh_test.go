package ssh

import (
	"context"
	"testing"
	"time"
)

func TestPollUntil_ImmediateSuccess(t *testing.T) {
	err := pollUntil(context.Background(), 10*time.Millisecond, time.Second, func(ctx context.Context) bool {
		return true
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPollUntil_SucceedsAfterRetries(t *testing.T) {
	calls := 0
	err := pollUntil(context.Background(), 10*time.Millisecond, time.Second, func(ctx context.Context) bool {
		calls++
		return calls >= 3
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls)
	}
}

func TestPollUntil_Timeout(t *testing.T) {
	err := pollUntil(context.Background(), 10*time.Millisecond, 50*time.Millisecond, func(ctx context.Context) bool {
		return false
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if got := err.Error(); got != "timed out after 50ms" {
		t.Errorf("error = %q, want timeout message", got)
	}
}

func TestPollUntil_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := pollUntil(ctx, 10*time.Millisecond, time.Second, func(ctx context.Context) bool {
		return false
	})
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestSSHArgs(t *testing.T) {
	t.Run("with key", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "/tmp/key")
		wantSuffix := []string{"-i", "/tmp/key", "pixel@10.0.0.1"}
		got := args[len(args)-3:]
		for i, w := range wantSuffix {
			if got[i] != w {
				t.Errorf("args[%d] = %q, want %q", len(args)-3+i, got[i], w)
			}
		}
	})

	t.Run("without key", func(t *testing.T) {
		args := sshArgs("10.0.0.1", "pixel", "")
		last := args[len(args)-1]
		if last != "pixel@10.0.0.1" {
			t.Errorf("last arg = %q, want %q", last, "pixel@10.0.0.1")
		}
		for _, a := range args {
			if a == "-i" {
				t.Error("should not include -i when keyPath is empty")
			}
		}
	})
}
