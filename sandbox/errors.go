package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// WrapNotFound translates upstream "not found" errors into a [ErrNotFound]
// chain so callers can use [errors.Is](err, [ErrNotFound]). Other errors
// pass through unchanged. Idempotent — wrapping an already-wrapped error
// returns it as-is.
//
// This is the single place that interprets upstream error message wording
// for both Incus and TrueNAS backends. Both libraries currently return
// English "not found" / "Not Found" phrasing. If a future backend uses a
// library with different wording, that backend can either:
//
//   - rely on this helper if its upstream library also uses English
//     "not found" phrasing,
//   - define its own wrapping helper, or
//   - this helper can be extended to accept additional matchers.
//
// The first time either upstream library exposes a typed IsNotFound
// predicate, switch the body to use that — call sites stay the same.
func WrapNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return err
	}
	msg := err.Error()
	// TrueNAS uses "does not exist"; Incus and HTTP layers use "not found"/"Not Found".
	if strings.Contains(msg, "not found") ||
		strings.Contains(msg, "Not Found") ||
		strings.Contains(msg, "does not exist") {
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	}
	return err
}
