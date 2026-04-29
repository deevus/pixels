package truenas

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/deevus/pixels/sandbox"
)

// ReadFile reads the file (or first maxBytes) into memory. If maxBytes>0
// and the file is larger, returns truncated=true.
func (t *TrueNAS) ReadFile(ctx context.Context, name, p string, maxBytes int64) ([]byte, bool, error) {
	var cmd []string
	if maxBytes > 0 {
		cmd = []string{"head", "-c", strconv.FormatInt(maxBytes, 10), "--", p}
	} else {
		cmd = []string{"cat", "--", p}
	}

	out, err := t.Output(ctx, name, cmd)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", p, err)
	}

	truncated := false
	if maxBytes > 0 && int64(len(out)) >= maxBytes {
		statOut, statErr := t.Output(ctx, name, []string{"stat", "-c", "%s", "--", p})
		if statErr != nil {
			// stat failed; conservatively assume truncation. We read exactly
			// maxBytes, so the file is at least that big.
			truncated = true
		} else if size, perr := strconv.ParseInt(strings.TrimSpace(string(statOut)), 10, 64); perr == nil && size > maxBytes {
			truncated = true
		}
	}
	return out, truncated, nil
}

// ListFiles uses `find -printf '%p\t%s\t%m\t%y\n'` to enumerate entries.
// Non-recursive uses -maxdepth 1.
func (t *TrueNAS) ListFiles(ctx context.Context, name, p string, recursive bool) ([]sandbox.FileEntry, error) {
	args := []string{"find", p, "-mindepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	if !recursive {
		args = []string{"find", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	}
	out, err := t.Output(ctx, name, args)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", p, err)
	}
	var entries []sandbox.FileEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		modeOct, _ := strconv.ParseUint(parts[2], 8, 32)
		entries = append(entries, sandbox.FileEntry{
			Path:  parts[0],
			Size:  size,
			Mode:  os.FileMode(modeOct),
			IsDir: parts[3] == "d",
		})
	}
	return entries, nil
}

// DeleteFile removes a single file. Use `exec rm -rf` for recursive deletes.
func (t *TrueNAS) DeleteFile(ctx context.Context, name, p string) error {
	code, err := t.Run(ctx, name, sandbox.ExecOpts{Cmd: []string{"rm", "--", p}})
	if err != nil {
		return fmt.Errorf("rm %s: %w", p, err)
	}
	if code != 0 {
		return fmt.Errorf("rm %s: exit %d", p, code)
	}
	return nil
}
