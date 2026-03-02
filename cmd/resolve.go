package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
)

const containerPrefix = "px-"

func containerName(name string) string {
	return containerPrefix + name
}

func displayName(name string) string {
	return strings.TrimPrefix(name, containerPrefix)
}

// resolveRunningIP returns the IP of a running container, checking the local
// cache first to avoid a WebSocket round-trip. Falls back to the sandbox
// interface for API lookups and updates the cache on success.
func resolveRunningIP(ctx context.Context, name string) (string, error) {
	if cached := cache.Get(name); cached != nil && cached.IP != "" && cached.Status == "RUNNING" {
		return cached.IP, nil
	}

	sb, err := openSandbox()
	if err != nil {
		return "", err
	}
	defer sb.Close()

	inst, err := sb.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", name, err)
	}
	if inst.Status != "RUNNING" {
		return "", fmt.Errorf("pixel %q is %s — start it first", name, inst.Status)
	}
	if len(inst.Addresses) == 0 {
		return "", fmt.Errorf("no IP address for %s", name)
	}
	ip := inst.Addresses[0]
	cache.Put(name, &cache.Entry{IP: ip, Status: inst.Status})
	return ip, nil
}

func newTabWriter(cmd *cobra.Command) *tabwriter.Writer {
	return tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
}

// readSSHPubKey reads the SSH public key from the path configured in ssh.key.
// It derives the .pub path from the private key path.
func readSSHPubKey() (string, error) {
	keyPath := cfg.SSH.Key
	if keyPath == "" {
		return "", nil
	}
	pubPath := keyPath + ".pub"
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("reading SSH public key %s: %w", pubPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}
