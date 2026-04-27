package mcp

import (
	"embed"

	"github.com/deevus/pixels/internal/config"
)

//go:embed bases/*.sh
var DefaultsFS embed.FS

// DefaultBases is the set of bases shipped with the binary. Loaded into
// config.MCP.Bases at startup if the user has not declared a base of the
// same name. User config wins on name conflict — no field merge.
var DefaultBases = map[string]config.Base{
	"dev": {
		ParentImage: "images:ubuntu/24.04",
		SetupScript: "mcp:bases/dev.sh",
		Description: "Ubuntu 24.04 + git, curl, wget, jq, vim, build-essential",
	},
	"python": {
		From:        "dev",
		SetupScript: "mcp:bases/python.sh",
		Description: "dev + python3, pip, pipx, venv",
	},
	"node": {
		From:        "dev",
		SetupScript: "mcp:bases/node.sh",
		Description: "dev + Node 22 LTS, npm",
	},
}
