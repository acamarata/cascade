package plugin

// Purpose: the production RegistryCache — one file per key under a
//   directory, written atomically (tmp file + os.Rename) so a crash or a
//   concurrent reader never observes a partially written entry.
// Inputs: a directory (Dir) plus a key on every call.
// Outputs: cached bytes, or ok=false when absent; never a partial file.
// Constraints: keys are validated against path traversal (no "/" or "\"),
//   since a caller-supplied key must never let a Put escape Dir.
// SPORT: pkg/plugin registry-client (ADD) — P1-E24-W5-S50-T1.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FileCache is the production RegistryCache.
type FileCache struct {
	// Dir is the writable directory cache entries are stored under.
	Dir string
}

var _ RegistryCache = FileCache{}

// Get implements RegistryCache.
func (c FileCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	path, err := c.path(key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, cascade.Wrapf(cascade.KindUnavailable, err, "read cache entry %q", key)
	}
	return data, true, nil
}

// Put implements RegistryCache: it writes to Dir/<key>.json.tmp, then
// renames into place, so readers only ever see the old or the new
// content, never a torn write.
func (c FileCache) Put(_ context.Context, key string, data []byte) error {
	path, err := c.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "create cache dir for %q", key)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "write cache tmp for %q", key)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return cascade.Wrapf(cascade.KindUnavailable, err, "rename cache tmp for %q", key)
	}
	return nil
}

func (c FileCache) path(key string) (string, error) {
	if key == "" || strings.ContainsAny(key, "/\\") {
		return "", cascade.Newf(cascade.KindInvalidInput, "invalid cache key %q", key)
	}
	return filepath.Join(c.Dir, key+".json"), nil
}
