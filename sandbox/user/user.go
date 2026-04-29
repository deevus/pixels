// Package user defines the unprivileged sandbox user that container file
// operations write as. Files written via the sandbox file APIs are chowned
// to this owner so subsequent exec calls (which also run as this user) can
// read and modify them. Hardcoded to match the convention established by the
// provisioning scripts, which create `pixel` at uid 1000.
package user

const (
	UID = 1000
	GID = 1000
)
