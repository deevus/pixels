package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	shellescape "al.essio.dev/pkg/shellescape"
)

// FilesViaExec implements [Files] by composing shell commands over an [Exec].
// Backends without a native file API can embed this struct to satisfy [Files].
//
// All commands assume a POSIX shell with `cat`, `head`, `find`, `rm`,
// `mkdir`, `chmod`, and `stat` available — which is true on every Linux
// container image we ship.
type FilesViaExec struct {
	Exec Exec
}

// WriteFile creates parent dirs, streams content via stdin into `cat > path`,
// then chmods to mode.
func (f FilesViaExec) WriteFile(ctx context.Context, name, p string, content []byte, mode os.FileMode) error {
	if dir := path.Dir(p); dir != "." && dir != "/" {
		if code, err := f.Exec.Run(ctx, name, ExecOpts{
			Cmd: []string{"mkdir", "-p", "--", dir},
		}); err != nil || code != 0 {
			return fmt.Errorf("mkdir %s: code=%d err=%v", dir, code, err)
		}
	}

	var stderr bytes.Buffer
	cmd := fmt.Sprintf("cat > %s", shellescape.Quote(p))
	code, err := f.Exec.Run(ctx, name, ExecOpts{
		Cmd:    []string{"sh", "-c", cmd},
		Stdin:  bytes.NewReader(content),
		Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	if code != 0 {
		return fmt.Errorf("write %s: exit %d: %s", p, code, stderr.String())
	}

	if code, err := f.Exec.Run(ctx, name, ExecOpts{
		Cmd: []string{"chmod", fmt.Sprintf("%o", mode), "--", p},
	}); err != nil || code != 0 {
		return fmt.Errorf("chmod %s: code=%d err=%v", p, code, err)
	}
	return nil
}

// ReadFile streams the file (or first maxBytes) to a buffer. If maxBytes>0
// and the file is larger, returns truncated=true.
func (f FilesViaExec) ReadFile(ctx context.Context, name, p string, maxBytes int64) ([]byte, bool, error) {
	var buf bytes.Buffer
	var cmd []string
	if maxBytes > 0 {
		cmd = []string{"head", "-c", strconv.FormatInt(maxBytes, 10), "--", p}
	} else {
		cmd = []string{"cat", "--", p}
	}

	code, err := f.Exec.Run(ctx, name, ExecOpts{Cmd: cmd, Stdout: &buf})
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", p, err)
	}
	if code != 0 {
		return nil, false, fmt.Errorf("read %s: exit %d", p, code)
	}

	truncated := false
	if maxBytes > 0 && int64(buf.Len()) >= maxBytes {
		out, err := f.Exec.Output(ctx, name, []string{"stat", "-c", "%s", "--", p})
		if err != nil {
			// stat failed; conservatively assume truncation. We read exactly
			// maxBytes, so the file is at least that big — treating it as
			// truncated is safe and visible to the caller.
			truncated = true
		} else if size, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); perr == nil && size > maxBytes {
			truncated = true
		}
	}
	return buf.Bytes(), truncated, nil
}

// ListFiles uses `find -printf '%p\t%s\t%m\t%y\n'` to enumerate entries.
// Non-recursive uses -maxdepth 1.
func (f FilesViaExec) ListFiles(ctx context.Context, name, p string, recursive bool) ([]FileEntry, error) {
	args := []string{"find", p, "-mindepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	if !recursive {
		args = []string{"find", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	}
	out, err := f.Exec.Output(ctx, name, args)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", p, err)
	}
	var entries []FileEntry
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
		entries = append(entries, FileEntry{
			Path:  parts[0],
			Size:  size,
			Mode:  os.FileMode(modeOct),
			IsDir: parts[3] == "d",
		})
	}
	return entries, nil
}

// DeleteFile removes a single file. Use `exec rm -rf` for recursive deletes.
func (f FilesViaExec) DeleteFile(ctx context.Context, name, p string) error {
	var stderr bytes.Buffer
	code, err := f.Exec.Run(ctx, name, ExecOpts{
		Cmd:    []string{"rm", "--", p},
		Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("rm %s: %w", p, err)
	}
	if code != 0 {
		return fmt.Errorf("rm %s: exit %d: %s", p, code, stderr.String())
	}
	return nil
}

// readAll is a small helper that lets test files reuse the same name without
// importing io directly.
func readAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}
