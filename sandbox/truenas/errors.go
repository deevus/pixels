package truenas

import (
	"errors"
	"fmt"
	"strings"

	"github.com/deevus/pixels/sandbox"
)

// wrapNotFound translates upstream "not found" errors into a sandbox.ErrNotFound
// chain so callers can use errors.Is(err, sandbox.ErrNotFound). Other errors
// pass through unchanged. Idempotent — wrapping an already-wrapped error
// returns it as-is.
//
// This is the single place that interprets upstream error message wording.
// If the truenas-go client ever exposes a typed IsNotFound predicate,
// switch the body to use that — call sites stay the same.
func wrapNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sandbox.ErrNotFound) {
		return err
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "Not Found") {
		return fmt.Errorf("%w: %s", sandbox.ErrNotFound, msg)
	}
	return err
}
