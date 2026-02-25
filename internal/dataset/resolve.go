package dataset

import "fmt"

// Resolve returns the ZFS dataset path for a container.
//
// If overridePrefix is set (from config checkpoint.dataset_prefix), it is used directly.
// Otherwise, the conventional path <pool>/incus/containers/<name> is returned.
func Resolve(pool, containerName, overridePrefix string) string {
	if overridePrefix != "" {
		return fmt.Sprintf("%s/%s", overridePrefix, containerName)
	}
	return fmt.Sprintf("%s/incus/containers/%s", pool, containerName)
}
