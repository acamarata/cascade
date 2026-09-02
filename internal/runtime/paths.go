package runtime

import (
	"fmt"
	"os"
	"path/filepath"
)

// Purpose: the canonical filesystem/socket path layout for the Cascade
//   binary, behind an injectable PathProvider interface.
// Inputs: an injected Getenv and HomeDirFunc (production callers use
//   NewDefaultPathProvider; tests must use NewPathProvider with fakes).
// Outputs: root/config/socket/data/log paths and per-profile storage
//   roots, all derived once at construction time.
// Constraints: T-1 contract — "No bare os.UserHomeDir() calls outside the
//   bootstrap layer." internal/runtime IS the bootstrap layer; every other
//   package must go through PathProvider. Art.7.1 — tests never touch the
//   real home or XDG dirs; NewPathProvider takes fakes for exactly this
//   reason.
// SPORT: runtime/paths (ADD, placeholder per T-1 sport_updates).

// Getenv matches os.Getenv's signature so callers can inject a fake
// environment in tests instead of mutating the real process environment.
type Getenv func(key string) string

// HomeDirFunc matches os.UserHomeDir's signature.
type HomeDirFunc func() (string, error)

// PathProvider resolves every filesystem and socket location the runtime
// needs. It is injectable so tests never touch the real user home or XDG
// dirs (Art.7.1).
type PathProvider interface {
	// Root is CASCADE_HOME, or ~/.cascade when unset.
	Root() string
	// ConfigPath is CASCADE_CONFIG, or Root()/config.toml when unset.
	ConfigPath() string
	// SocketPath is CASCADE_SOCKET, or Root()/daemon.sock when unset
	// (R-14.94: env override resolves here, with a derived default).
	SocketPath() string
	// DataDir is Root()/data.
	DataDir() string
	// LogDir is Root()/logs.
	LogDir() string
	// StorageRoot is the per-profile storage root under DataDir, e.g.
	// Root()/data/storage/local.
	StorageRoot(p Profile) string
}

type pathProvider struct {
	root       string
	configPath string
	socketPath string
}

// NewPathProvider builds a PathProvider from an injected environment
// accessor and home-directory resolver. It performs no filesystem I/O of
// its own beyond calling homeDir when CASCADE_HOME is unset. A nil getenv
// falls back to os.Getenv and a nil homeDir falls back to os.UserHomeDir —
// production code should prefer the explicit NewDefaultPathProvider so the
// fallback is visible at the call site; tests must always pass real fakes.
func NewPathProvider(getenv Getenv, homeDir HomeDirFunc) (PathProvider, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}

	root := getenv("CASCADE_HOME")
	if root == "" {
		home, err := homeDir()
		if err != nil {
			return nil, fmt.Errorf("runtime: resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".cascade")
	}

	configPath := getenv("CASCADE_CONFIG")
	if configPath == "" {
		configPath = filepath.Join(root, "config.toml")
	}

	socketPath := getenv("CASCADE_SOCKET")
	if socketPath == "" {
		socketPath = filepath.Join(root, "daemon.sock")
	}

	return &pathProvider{root: root, configPath: configPath, socketPath: socketPath}, nil
}

// NewDefaultPathProvider builds a PathProvider against the real process
// environment and OS home-directory resolver. Only production entrypoints
// (bootstrap.go and above) should call this; tests must use NewPathProvider
// with injected fakes instead (Art.7.1).
func NewDefaultPathProvider() (PathProvider, error) {
	return NewPathProvider(os.Getenv, os.UserHomeDir)
}

func (p *pathProvider) Root() string       { return p.root }
func (p *pathProvider) ConfigPath() string { return p.configPath }
func (p *pathProvider) SocketPath() string { return p.socketPath }
func (p *pathProvider) DataDir() string    { return filepath.Join(p.root, "data") }
func (p *pathProvider) LogDir() string     { return filepath.Join(p.root, "logs") }

func (p *pathProvider) StorageRoot(prof Profile) string {
	return filepath.Join(p.DataDir(), "storage", string(prof))
}
