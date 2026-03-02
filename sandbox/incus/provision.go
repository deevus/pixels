package incus

import (
	"bytes"
	"fmt"
	"io"

	incusclient "github.com/lxc/incus/v6/client"
)

// pushFile writes a file into a running container via the Incus file API.
func (i *Incus) pushFile(name, path string, content []byte, mode int) error {
	return i.server.CreateInstanceFile(name, path, incusclient.InstanceFileArgs{
		Content:   bytes.NewReader(content),
		UID:       0,
		GID:       0,
		Mode:      mode,
		Type:      "file",
		WriteMode: "overwrite",
	})
}

// pushFileOwned writes a file with specific ownership.
func (i *Incus) pushFileOwned(name, path string, content []byte, mode int, uid, gid int64) error {
	return i.server.CreateInstanceFile(name, path, incusclient.InstanceFileArgs{
		Content:   bytes.NewReader(content),
		UID:       uid,
		GID:       gid,
		Mode:      mode,
		Type:      "file",
		WriteMode: "overwrite",
	})
}

// mkdir creates a directory inside a container via the Incus file API.
func (i *Incus) mkdir(name, path string, mode int) error {
	return i.server.CreateInstanceFile(name, path, incusclient.InstanceFileArgs{
		UID:  0,
		GID:  0,
		Mode: mode,
		Type: "directory",
	})
}

// readFile reads a file from inside a container.
func (i *Incus) readFile(name, path string) ([]byte, error) {
	rc, _, err := i.server.GetInstanceFile(name, path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
