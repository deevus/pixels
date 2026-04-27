package config

// DefaultBases is the set of bases shipped with the binary. Lives here
// (not internal/mcp) so config.Load() can merge them without importing
// internal/mcp (which would be a cycle). The embedded script *files*
// stay in internal/mcp/defaults.go via //go:embed.
var DefaultBases = map[string]Base{
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
