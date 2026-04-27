package mcp

import "github.com/deevus/pixels/internal/config"

// DefaultBasePrefix is the fallback when cfg.MCP.BasePrefix is empty.
const DefaultBasePrefix = "px-base-"

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

// BuilderContainerName returns the fixed container name that holds the
// snapshot for the given base. This is the name of the stopped builder
// container that BuildBase creates and keeps alive.
func BuilderContainerName(baseName string) string { return "px-base-builder-" + baseName }
