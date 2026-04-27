package mcp

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLoggerVerboseEmitsDebug(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, true)
	log.Debug("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("verbose logger should emit Debug; got %q", buf.String())
	}
}

func TestNewLoggerNonVerboseDropsDebug(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, false)
	log.Debug("hello")
	if buf.Len() != 0 {
		t.Errorf("non-verbose logger should drop Debug; got %q", buf.String())
	}
}

func TestNewLoggerAlwaysEmitsErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, false)
	log.Error("oops")
	if !strings.Contains(buf.String(), "oops") {
		t.Errorf("non-verbose logger should still emit Error; got %q", buf.String())
	}
}

func TestNopLoggerDoesNotPanic(t *testing.T) {
	log := NopLogger()
	log.Info("anything")
	log.Error("anything")
	// no assertion — just must not panic
}
