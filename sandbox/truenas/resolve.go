package truenas

import (
	"context"
	"fmt"
	"strings"

	tnapi "github.com/deevus/truenas-go"
)

const containerPrefix = "px-"

// prefixed prepends the container prefix to a bare name.
func prefixed(name string) string {
	return containerPrefix + name
}

// unprefixed strips the container prefix from a full name.
func unprefixed(name string) string {
	return strings.TrimPrefix(name, containerPrefix)
}

// ensureRunning verifies the container is running and has a network address.
func (t *TrueNAS) ensureRunning(ctx context.Context, name string) error {
	full := prefixed(name)

	instance, err := t.client.Virt.GetInstance(ctx, full)
	if err != nil {
		return fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance == nil {
		return fmt.Errorf("instance %q not found", name)
	}
	if instance.Status != "RUNNING" {
		return fmt.Errorf("instance %q is %s — start it first", name, instance.Status)
	}

	if ipFromAliases(instance.Aliases) == "" {
		return fmt.Errorf("no IP address for %s", name)
	}
	return nil
}

// ipFromAliases extracts the first IPv4 address from a VirtInstance's aliases.
func ipFromAliases(aliases []tnapi.VirtAlias) string {
	for _, a := range aliases {
		if a.Type == "INET" || a.Type == "ipv4" {
			return a.Address
		}
	}
	return ""
}
