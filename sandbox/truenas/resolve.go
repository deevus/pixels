package truenas

import (
	"context"
	"fmt"
	"strings"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/cache"
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

// resolveRunningIP returns the IP of a running container, checking the local
// cache first (keyed by bare name) then falling back to the API.
func (t *TrueNAS) resolveRunningIP(ctx context.Context, name string) (string, error) {
	full := prefixed(name)

	if cached := cache.Get(name); cached != nil && cached.IP != "" && cached.Status == "RUNNING" {
		return cached.IP, nil
	}

	instance, err := t.client.Virt.GetInstance(ctx, full)
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance == nil {
		return "", fmt.Errorf("instance %q not found", name)
	}
	if instance.Status != "RUNNING" {
		return "", fmt.Errorf("instance %q is %s — start it first", name, instance.Status)
	}

	ip := ipFromAliases(instance.Aliases)
	if ip == "" {
		return "", fmt.Errorf("no IP address for %s", name)
	}
	cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status})
	return ip, nil
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
