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

// ensureRunning verifies the container is running and has a network address,
// returning the instance for callers that need its metadata (e.g. IP).
func (t *TrueNAS) ensureRunning(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
	full := prefixed(name)

	instance, err := t.client.Virt.GetInstance(ctx, full)
	if err != nil {
		return nil, fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance == nil {
		return nil, fmt.Errorf("instance %q not found", name)
	}
	if instance.Status != "RUNNING" {
		return nil, fmt.Errorf("instance %q is %s — start it first", name, instance.Status)
	}

	if ipFromAliases(instance.Aliases) == "" {
		return nil, fmt.Errorf("no IP address for %s", name)
	}
	return instance, nil
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
