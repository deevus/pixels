package incus

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/deevus/pixels/sandbox"
)

// WriteFile writes content to path inside the container via the native Incus
// file API. Parents are created as 0o755 directories (mkdir-p semantics).
// uid/gid set ownership; pass [sandbox.NoOwner] (negative) to leave the file
// root-owned (the filesystem-API default).
func (i *Incus) WriteFile(ctx context.Context, name, p string, content []byte, mode os.FileMode, uid, gid int) error {
	full := prefixed(name)

	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := i.mkdirP(full, dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if uid < 0 || gid < 0 {
		return i.pushFile(full, p, content, int(mode))
	}
	return i.pushFileOwned(full, p, content, int(mode), int64(uid), int64(gid))
}

// ReadFile streams the file (or first maxBytes) into memory via the native
// Incus file API. If maxBytes>0 and the file is larger, returns truncated=true.
func (i *Incus) ReadFile(ctx context.Context, name, p string, maxBytes int64) ([]byte, bool, error) {
	full := prefixed(name)
	rc, _, err := i.server.GetInstanceFile(full, p)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", p, err)
	}
	defer rc.Close()

	if maxBytes <= 0 {
		body, err := io.ReadAll(rc)
		if err != nil {
			return nil, false, fmt.Errorf("read %s: %w", p, err)
		}
		return body, false, nil
	}

	// Read maxBytes+1 so we can detect truncation without a separate stat call.
	body, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", p, err)
	}
	if int64(len(body)) > maxBytes {
		return body[:maxBytes], true, nil
	}
	return body, false, nil
}

// ListFiles enumerates entries via shell `find -printf` since the Incus file
// API doesn't expose size in directory listings.
func (i *Incus) ListFiles(ctx context.Context, name, p string, recursive bool) ([]sandbox.FileEntry, error) {
	args := []string{"find", p, "-mindepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	if !recursive {
		args = []string{"find", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	}
	out, err := i.Output(ctx, name, args)
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

// DeleteFile removes a single file via the native Incus file API.
func (i *Incus) DeleteFile(ctx context.Context, name, p string) error {
	full := prefixed(name)
	if err := i.server.DeleteInstanceFile(full, p); err != nil {
		return fmt.Errorf("delete %s: %w", p, err)
	}
	return nil
}

// mkdirP creates each ancestor directory along p with the given mode. Errors
// from individual mkdir calls are ignored — if a directory already exists the
// API returns an error we don't care about, and if a real failure happens
// the subsequent file write will surface it with a clearer message.
func (i *Incus) mkdirP(name, p string, mode int) error {
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	cur := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		cur += "/" + part
		_ = i.mkdir(name, cur, mode)
	}
	return nil
}
