package sandbox

import "fmt"

// Factory constructs a Sandbox from backend-specific configuration.
// The cfg map is interpreted by each backend (e.g. "host" and "api_key"
// for TrueNAS, "socket" for Incus).
type Factory func(cfg map[string]string) (Sandbox, error)

var backends = map[string]Factory{}

// Register makes a backend available by name. It is intended to be called
// from init() in backend implementation packages.
func Register(name string, f Factory) {
	backends[name] = f
}

// Open returns a Sandbox for the named backend, configured with cfg.
// If no backend is registered under that name, it returns an error.
func Open(name string, cfg map[string]string) (Sandbox, error) {
	f, ok := backends[name]
	if !ok {
		return nil, fmt.Errorf("sandbox: unknown backend %q", name)
	}
	return f(cfg)
}
