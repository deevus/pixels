package mcp

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNewServerMountsHandler(t *testing.T) {
	dir := t.TempDir()
	st, _ := LoadState(filepath.Join(dir, "s.json"))
	be := newFakeSandbox()

	mux, _ := NewServer(ServerOpts{
		State:          st,
		Backend:        be,
		Prefix:         "px-mcp-",
		ExecTimeoutMax: 10 * time.Minute,
	}, "/mcp")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Errorf("unexpected 5xx: %d", resp.StatusCode)
	}
}
