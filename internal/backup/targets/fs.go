// Purpose: FSTarget (05 §Epic S S-41.T3) — the local-filesystem-root
// backup Target: the default target, and the one every S-41.T2 snapshot
// flow and S-41.T4 integrity/restore test exercises directly against a
// real filesystem (t.TempDir(), Art.7.1).
//
// Inputs: a root directory (NewFSTarget) plus a key/reader per call.
// Outputs: real files under root, laid out exactly as
// internal/backup/repo.go's key prefixes name them.
// Constraints: every write goes through runtime.WriteReaderAtomic
// (temp, fsync, rename, directory fsync) so a crash mid-write never leaves
// a partially-written object visible to a concurrent Get;
// every key is resolved and bounds-checked against root before any path
// operation, so a key can never escape it.
//
// SPORT: internal.backup.targets.fs/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tmpFilePrefix marked a not-yet-renamed write before writes moved to
// runtime.WriteReaderAtomic; List still skips one a crash left behind,
// alongside the helper's own temp names (runtime.IsAtomicTempName).
const tmpFilePrefix = ".fs-target-tmp-"

// FSTarget is the local-filesystem-root Target driver. The zero value is
// not usable; construct with NewFSTarget.
type FSTarget struct {
	root string
}

// NewFSTarget returns an FSTarget rooted at root, creating root if it
// does not already exist.
func NewFSTarget(root string) (*FSTarget, error) {
	if strings.TrimSpace(root) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "targets: fs target root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "targets: fs: resolving root")
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "targets: fs: creating root")
	}
	return &FSTarget{root: abs}, nil
}

// resolve turns key into a path under t.root, refusing any key that is
// empty, carries a ".." segment, or (after joining) resolves outside
// root.
func (t *FSTarget) resolve(key string) (string, error) {
	if key == "" || strings.Contains(key, "..") {
		return "", cascade.Newf(cascade.KindInvalidInput, "targets: fs: invalid key %q", key)
	}
	full := filepath.Join(t.root, filepath.FromSlash(key))
	if full != t.root && !strings.HasPrefix(full, t.root+string(filepath.Separator)) {
		return "", cascade.Newf(cascade.KindInvalidInput, "targets: fs: key %q escapes root", key)
	}
	return full, nil
}

// Put implements Target.
func (t *FSTarget) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	full, err := t.resolve(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "targets: fs: creating parent directory")
	}
	// runtime.WriteReaderAtomic streams r into a temp file beside full,
	// fsyncs it, renames it into place and fsyncs dir: a reader never
	// observes a partial object and a crash never publishes one. 0600
	// matches the owner-only mode objects have always had.
	return runtime.WriteReaderAtomic(full, r, 0o600)
}

// Get implements Target.
func (t *FSTarget) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	full, err := t.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cascade.Newf(cascade.KindNotFound, "targets: fs: %q not found", key)
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "targets: fs: opening")
	}
	return f, nil
}

// List implements Target: a recursive walk under root, returning every
// regular (non-temp) file's slash-form key that has prefix.
func (t *FSTarget) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	var out []string
	err := filepath.WalkDir(t.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), tmpFilePrefix) || runtime.IsAtomicTempName(d.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(t.root, path)
		if rerr != nil {
			return rerr
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "targets: fs: listing")
	}
	sort.Strings(out)
	return out, nil
}

// Delete implements Target. Deleting an absent key is not an error.
func (t *FSTarget) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	full, err := t.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return cascade.Wrap(cascade.KindUnavailable, err, "targets: fs: deleting")
	}
	return nil
}
