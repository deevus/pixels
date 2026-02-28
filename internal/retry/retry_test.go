package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Poll tests
// ---------------------------------------------------------------------------

func TestPoll_SuccessOnFirstCheck(t *testing.T) {
	err := Poll(context.Background(), 10*time.Millisecond, time.Second, func(_ context.Context) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestPoll_SuccessAfterSeveralChecks(t *testing.T) {
	calls := 0
	err := Poll(context.Background(), 10*time.Millisecond, time.Second, func(_ context.Context) (bool, error) {
		calls++
		return calls >= 3, nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 calls, got %d", calls)
	}
}

func TestPoll_Timeout(t *testing.T) {
	err := Poll(context.Background(), 10*time.Millisecond, 50*time.Millisecond, func(_ context.Context) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
}

func TestPoll_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Poll(ctx, 10*time.Millisecond, time.Second, func(_ context.Context) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPoll_FatalErrorStopsPolling(t *testing.T) {
	fatal := errors.New("fatal failure")
	calls := 0
	err := Poll(context.Background(), 10*time.Millisecond, time.Second, func(_ context.Context) (bool, error) {
		calls++
		return false, fatal
	})
	if !errors.Is(err, fatal) {
		t.Fatalf("expected fatal error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

// ---------------------------------------------------------------------------
// Do tests
// ---------------------------------------------------------------------------

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	err := Do(context.Background(), 3, 10*time.Millisecond, func(_ context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestDo_SuccessAfterRetries(t *testing.T) {
	calls := 0
	err := Do(context.Background(), 5, 10*time.Millisecond, func(_ context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_AllAttemptsExhausted(t *testing.T) {
	lastErr := errors.New("persistent failure")
	err := Do(context.Background(), 3, 10*time.Millisecond, func(_ context.Context) error {
		return lastErr
	})
	if !errors.Is(err, lastErr) {
		t.Fatalf("expected last error, got %v", err)
	}
}

func TestDo_ContextCancellationDuringDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Do(ctx, 5, time.Second, func(_ context.Context) error {
		calls++
		if calls == 1 {
			// Cancel during the delay before the next retry.
			cancel()
		}
		return errors.New("fail")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancellation, got %d", calls)
	}
}
