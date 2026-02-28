package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Entry holds cached state for a pixel.
type Entry struct {
	IP     string `json:"ip"`
	Status string `json:"status"`
}

// dir returns the cache directory path.
func dir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "pixels")
	}
	d, _ := os.UserCacheDir()
	return filepath.Join(d, "pixels")
}

func path(name string) string {
	return filepath.Join(dir(), name+".json")
}

// Get reads a cached entry for the given pixel name.
// Returns nil if not cached.
func Get(name string) *Entry {
	data, err := os.ReadFile(path(name))
	if err != nil {
		return nil
	}
	var e Entry
	if json.Unmarshal(data, &e) != nil {
		return nil
	}
	return &e
}

// Put writes a cache entry for the given pixel name.
func Put(name string, e *Entry) {
	_ = os.MkdirAll(dir(), 0o755)
	data, _ := json.Marshal(e)
	_ = os.WriteFile(path(name), data, 0o644)
}

// Delete removes the cache entry for the given pixel name.
func Delete(name string) {
	_ = os.Remove(path(name))
}
