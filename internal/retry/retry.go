package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrTimeout is returned by Poll when the deadline expires without success.
var ErrTimeout = errors.New("poll timed out")

// Poll calls fn at the given interval until it returns (true, nil), a non-nil
// error (fatal — stop immediately), or the timeout/context expires.
func Poll(ctx context.Context, interval, timeout time.Duration, fn func(ctx context.Context) (bool, error)) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		done, err := fn(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("%w after %s", ErrTimeout, timeout)
		case <-ticker.C:
		}
	}
}

// Do calls fn up to attempts times, waiting delay between retries.
// It returns nil on first success, or the last error if all attempts fail.
// The delay between retries is context-aware.
func Do(ctx context.Context, attempts int, delay time.Duration, fn func(ctx context.Context) error) error {
	var lastErr error
	for i := range attempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}
