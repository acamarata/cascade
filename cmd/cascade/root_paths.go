// Purpose: lazyPaths, the deferred runtime.PathProvider adapter the config
//
//	and daemon command trees hold instead of resolving paths eagerly.
//
// Inputs: none at construction; each accessor resolves runtime.PathProvider
//
//	on first use.
//
// Outputs: the individual path strings a command needs (config path, socket
//
//	path, data dir, ...), or the empty string when resolution fails.
//
// Constraints: split out of root.go to keep that file under the 300-line
//
//	cap (Art.10.3) as probeDaemonlessAndAttach grew its `daemon run`
//	suppression case; this type has no dependency on anything else in
//	root.go, so the split carries no behavior change.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import "github.com/acamarata/cascade/internal/runtime"

// lazyPaths defers NewDefaultPathProvider to first use, so constructing the
// command tree never touches the environment. Each accessor resolves on
// demand and returns the zero value if resolution fails; the config commands
// validate the paths they receive and report the failure themselves.
type lazyPaths struct{}

func (lazyPaths) resolve() runtime.PathProvider {
	p, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return nil
	}
	return p
}

func (l lazyPaths) get(f func(runtime.PathProvider) string) string {
	p := l.resolve()
	if p == nil {
		return ""
	}
	return f(p)
}

func (l lazyPaths) Root() string {
	return l.get(func(p runtime.PathProvider) string { return p.Root() })
}
func (l lazyPaths) ConfigPath() string {
	return l.get(func(p runtime.PathProvider) string { return p.ConfigPath() })
}
func (l lazyPaths) SocketPath() string {
	return l.get(func(p runtime.PathProvider) string { return p.SocketPath() })
}
func (l lazyPaths) DataDir() string {
	return l.get(func(p runtime.PathProvider) string { return p.DataDir() })
}
func (l lazyPaths) LogDir() string {
	return l.get(func(p runtime.PathProvider) string { return p.LogDir() })
}
func (l lazyPaths) StorageRoot(profile runtime.Profile) string {
	return l.get(func(p runtime.PathProvider) string { return p.StorageRoot(profile) })
}
