package mcp

import (
	"io"
	"log/slog"
)

// NewLogger returns a slog.Logger that writes to w. When verbose is true
// the level is Debug; otherwise Info. Format is text (one line per record)
// — structured but human-readable for daemon stderr.
func NewLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

// NopLogger returns a logger that discards everything. Use in tests where
// log output isn't being asserted on.
func NopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
