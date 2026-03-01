package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	truenas "github.com/deevus/truenas-go"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/ssh"
	tnc "github.com/deevus/pixels/internal/truenas"
)

const containerPrefix = "px-"

func connectClient(ctx context.Context) (*tnc.Client, error) {
	if cfg.TrueNAS.Host == "" {
		return nil, fmt.Errorf("TrueNAS host not configured — set truenas.host in config or use --host")
	}
	if cfg.TrueNAS.APIKey == "" {
		return nil, fmt.Errorf("TrueNAS API key not configured — set truenas.api_key in config or use --api-key")
	}
	return tnc.Connect(ctx, cfg)
}

func containerName(name string) string {
	return containerPrefix + name
}

func displayName(name string) string {
	return strings.TrimPrefix(name, containerPrefix)
}

func resolveIP(instance *truenas.VirtInstance) string {
	for _, a := range instance.Aliases {
		if a.Type == "INET" || a.Type == "ipv4" {
			return a.Address
		}
	}
	return ""
}

// resolveRunningIP returns the IP of a running container, checking the local
// cache first to avoid a WebSocket round-trip. It updates the cache on a
// successful API lookup.
func resolveRunningIP(ctx context.Context, name string) (string, error) {
	if cached := cache.Get(name); cached != nil && cached.IP != "" && cached.Status == "RUNNING" {
		return cached.IP, nil
	}

	client, err := connectClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	return lookupRunningIP(ctx, client.Virt, name)
}

// lookupRunningIP fetches a container via the Virt API, verifies it is running,
// and returns its IP. It caches the result on success.
func lookupRunningIP(ctx context.Context, virt truenas.VirtServiceAPI, name string) (string, error) {
	instance, err := virt.GetInstance(ctx, containerName(name))
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance == nil {
		return "", fmt.Errorf("pixel %q not found", name)
	}
	if instance.Status != "RUNNING" {
		return "", fmt.Errorf("pixel %q is %s — start it first", name, instance.Status)
	}

	ip := resolveIP(instance)
	if ip == "" {
		return "", fmt.Errorf("no IP address for %s", name)
	}
	cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status})
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

// ensureSSHAuth tests key auth and, if it fails, writes the current machine's
// SSH public key to the container's authorized_keys via TrueNAS.
func ensureSSHAuth(cmd *cobra.Command, ctx context.Context, ip, name string) error {
	if err := ssh.TestAuth(ctx, ssh.ConnConfig{Host: ip, User: cfg.SSH.User, KeyPath: cfg.SSH.Key}); err == nil {
		return nil
	}

	pubKey, err := readSSHPubKey()
	if err != nil {
		return err
	}
	if pubKey == "" {
		return fmt.Errorf("SSH key auth failed and no public key configured")
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "SSH key not authorized, updating...\n")

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	return client.WriteAuthorizedKey(ctx, containerName(name), pubKey)
}
