package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestBuildChainBuildsBottomUp(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}

	var built []string
	exists := func(container string) bool { return false }
	build := func(name string) error {
		built = append(built, name)
		return nil
	}

	if err := BuildChain(context.Background(), cfg, "python", exists, build); err != nil {
		t.Fatalf("BuildChain: %v", err)
	}
	if want := []string{"dev", "python"}; !equalStrings(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
}

func TestBuildChainSkipsExistingLinks(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}

	var built []string
	exists := func(container string) bool { return container == "px-base-dev" }
	build := func(name string) error {
		built = append(built, name)
		return nil
	}
	if err := BuildChain(context.Background(), cfg, "python", exists, build); err != nil {
		t.Fatalf("BuildChain: %v", err)
	}
	if want := []string{"python"}; !equalStrings(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
}

func TestBuildChainErrorsOnUnknownTarget(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{Bases: map[string]config.Base{}}}
	err := BuildChain(context.Background(), cfg, "ghost", func(string) bool { return false }, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for unknown target base")
	}
}

func TestBuildChainShortCircuitsOnBuildError(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}
	wantErr := errors.New("nope")
	build := func(name string) error {
		if name == "dev" {
			return wantErr
		}
		t.Fatalf("python should not have been attempted after dev failed")
		return nil
	}
	err := BuildChain(context.Background(), cfg, "python", func(string) bool { return false }, build)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wraps %v", err, wantErr)
	}
}

func TestBuildChainHonorsContextCancel(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var built []string
	build := func(name string) error {
		built = append(built, name)
		return nil
	}
	err := BuildChain(ctx, cfg, "python", func(string) bool { return false }, build)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(built) != 0 {
		t.Errorf("built = %v, want none (loop should bail before first build)", built)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
