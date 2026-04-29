package mcp

import "embed"

//go:embed bases/*.sh
var DefaultsFS embed.FS
