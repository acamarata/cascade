package ci

import "errors"

// ErrNotImplemented is returned by unimplemented stubs in the ci package.
var ErrNotImplemented = errors.New("not implemented")

// MemoKey is a deterministic cache key identifying a memoized build step.
type MemoKey struct {
	// Toolchain identifies the toolchain used to produce the cached result.
	Toolchain string
}

// ComputeMemoKey returns a deterministic cache key for the given toolchain
// and base inputs. An empty toolchain yields a zero-value key, which
// indicates that caching is disabled.
func ComputeMemoKey(toolchain, base string) (MemoKey, error) {
	if toolchain == "" {
		return MemoKey{}, nil
	}
	return MemoKey{Toolchain: toolchain}, nil
}
