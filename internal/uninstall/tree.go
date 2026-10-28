package uninstall

// Purpose: empty a directory tree while keeping every backup path in it.
// Inputs: the directory and Deps (for IsBackupPath).
// Outputs: removeTree's kept paths and joined error.
// Constraints: every operation goes through an os.Root at the directory,
//   so a symlink inside the tree is removed as a link and never followed
//   out of it. An entry that is a backup path, or a directory holding one,
//   is kept; everything else is removed. A failed entry does not stop the
//   walk.
// SPORT: internal/uninstall removeTree/ADDED.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// removeTree removes everything under dir except backup paths, then dir
// itself when nothing was kept.
func removeTree(dir string, d Deps) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: open %s", dir)
	}
	kept, errs := removeChildren(root, dir, ".", d)
	_ = root.Close()
	if len(kept) == 0 && len(errs) == 0 {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return kept, cascade.Wrapf(cascade.KindUnavailable, errors.Join(errs...), "uninstall: empty %s", dir)
	}
	return kept, nil
}

// removeChildren removes rel's entries under root; base is root's path.
func removeChildren(root *os.Root, base, rel string, d Deps) ([]string, []error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, []error{err}
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return nil, []error{err}
	}
	var kept []string
	var errs []error
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		full := filepath.Join(base, child)
		if IsBackupPath(full, d) {
			kept = append(kept, full)
			continue
		}
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			k, es := removeChildren(root, base, child, d)
			kept, errs = append(kept, k...), append(errs, es...)
			if len(k) > 0 || len(es) > 0 {
				continue
			}
		}
		if err := root.Remove(child); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return kept, errs
}

// joinPaths lists paths for a note.
func joinPaths(paths []string) string { return strings.Join(paths, ", ") }
