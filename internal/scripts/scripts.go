// Package scripts embeds the shell scripts shared across sandbox backends.
package scripts

import _ "embed"

//go:embed rc-local.sh
var RcLocal string

//go:embed setup-devtools.sh
var SetupDevtools string

//go:embed setup-egress.sh
var SetupEgress string

//go:embed enable-egress.sh
var EnableEgress string

//go:embed pixels-profile.sh
var PixelsProfile string
