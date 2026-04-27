package mcp

import "github.com/deevus/pixels/internal/config"

// DefaultBasePrefix is the fallback when cfg.MCP.BasePrefix is empty.
// Backends unconditionally prepend "px-" via prefixed(); this prefix sits
// *inside* that namespace, so the on-disk name is "px-base-<name>".
const DefaultBasePrefix = "base-"

// BaseName returns the container name for a base. The prefix is taken from
// cfg.MCP.BasePrefix, falling back to DefaultBasePrefix when empty (e.g. in
// tests that build a Config by hand).
func BaseName(cfg *config.Config, name string) string {
	prefix := cfg.MCP.BasePrefix
	if prefix == "" {
		prefix = DefaultBasePrefix
	}
	return prefix + name
}

