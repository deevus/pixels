package mcp

import (
	"context"
	"fmt"

	"github.com/deevus/pixels/internal/config"
)

// BuildChain ensures the dependency chain for `target` is built. It walks
// the `from` chain bottom-up; for any link whose container does not exist
// (per `exists`), `build(name)` is invoked. Build failures short-circuit
// (children of a failed parent are not attempted).
//
// `exists(container)` is called with the fully-prefixed container name
// (BaseName(cfg, n)). `build(name)` is called with the bare base name.
func BuildChain(ctx context.Context, cfg *config.Config, target string, exists func(container string) bool, build func(name string) error) error {
	bases := cfg.MCP.Bases
	if _, ok := bases[target]; !ok {
		return fmt.Errorf("base %q is not declared in config", target)
	}

	// Walk from-chain to root, collecting names in build order (root first).
	var order []string
	seen := map[string]bool{}
	cur := target
	for {
		if seen[cur] {
			// Should never happen — config validation rejects cycles. Guard anyway.
			return fmt.Errorf("base %q: cycle detected during chain walk", cur)
		}
		seen[cur] = true
		order = append([]string{cur}, order...)
		b, ok := bases[cur]
		if !ok {
			return fmt.Errorf("base %q: missing from config (config validation should have caught this)", cur)
		}
		if b.From == "" {
			break
		}
		cur = b.From
	}

	for _, name := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if exists(BaseName(cfg, name)) {
			continue
		}
		if err := build(name); err != nil {
			return fmt.Errorf("build %s: %w", name, err)
		}
	}
	return nil
}
