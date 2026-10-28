package uninstall

// Purpose: IsBackupPath, the guard Inventory and every path-based Remove
//   consult so no backup is ever an uninstall item or removed as part of
//   one.
// Inputs: a path and Deps (the cascade root, the data dir, the configured
//   filesystem backup targets).
// Outputs: true when the path is, or is inside, a backup location.
// Constraints: fail closed. A path is a backup path when it is at or under
//   <data>/backups or a configured target, when any component below the
//   cascade root or data dir is named "backups" (the store keeps migration
//   backups beside its database), or when the targets cannot be listed.
//   Both the lexical path and its symlink-resolved form are checked, and
//   every comparison ignores case, so a case-folding volume cannot slip a
//   backup past the guard.
// SPORT: internal/uninstall IsBackupPath/ADDED.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// backupDirName is the directory every in-tree backup lives under.
const backupDirName = "backups"

// IsBackupPath reports whether p is a backup location or inside one. A
// relative p is not a filesystem locator and reports false; every
// path-based Remove refuses a relative locator before it gets here.
func IsBackupPath(p string, d Deps) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	roots, ok := backupRoots(d)
	if !ok {
		return true
	}
	bases := treeBases(d)
	for _, cand := range resolvedForms(p) {
		for _, r := range roots {
			if within(cand, r) {
				return true
			}
		}
		for _, b := range bases {
			if hasBackupComponent(cand, b) {
				return true
			}
		}
	}
	return false
}

// backupRoots is <data>/backups plus every configured target, each in its
// lexical and resolved forms. ok is false when the targets cannot be read.
func backupRoots(d Deps) ([]string, bool) {
	var raw []string
	if d.Paths != nil {
		raw = append(raw, filepath.Join(d.Paths.DataDir(), backupDirName))
	}
	if d.BackupTargets != nil {
		targets, err := d.BackupTargets()
		if err != nil {
			return nil, false
		}
		for _, t := range targets {
			if t == "" {
				continue
			}
			abs, err := filepath.Abs(t)
			if err != nil {
				return nil, false
			}
			raw = append(raw, abs)
		}
	}
	var out []string
	for _, r := range raw {
		out = append(out, resolvedForms(r)...)
	}
	return out, true
}

// treeBases are the trees whose "backups" components are backups.
func treeBases(d Deps) []string {
	if d.Paths == nil {
		return nil
	}
	var out []string
	for _, b := range []string{d.Paths.Root(), d.Paths.DataDir()} {
		out = append(out, resolvedForms(b)...)
	}
	return out
}

// resolvedForms is p cleaned, plus p with its longest existing prefix
// resolved through symlinks when that differs.
func resolvedForms(p string) []string {
	clean := filepath.Clean(p)
	out := []string{clean}
	if r := resolveExisting(clean); r != "" && r != clean {
		out = append(out, r)
	}
	return out
}

// resolveExisting resolves the longest existing prefix of p and re-appends
// the rest; "" when nothing resolves.
func resolveExisting(p string) string {
	rest := ""
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// within reports whether p is root or below it, ignoring case.
func within(p, root string) bool {
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// hasBackupComponent reports whether any component of p below base is
// named "backups", ignoring case.
func hasBackupComponent(p, base string) bool {
	if !within(p, base) {
		return false
	}
	rel, err := filepath.Rel(strings.ToLower(base), strings.ToLower(p))
	if err != nil {
		return true
	}
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c == backupDirName {
			return true
		}
	}
	return false
}

// memoTargets wraps d.BackupTargets so one Inventory lists the targets
// once, however many items consult the guard.
func memoTargets(d Deps) Deps {
	if d.BackupTargets == nil {
		return d
	}
	inner := d.BackupTargets
	var once sync.Once
	var targets []string
	var err error
	d.BackupTargets = func() ([]string, error) {
		once.Do(func() { targets, err = inner() })
		return targets, err
	}
	return d
}

// lstatRegular reports whether p exists as a regular file (not a symlink).
// exists is false only when p is definitely absent.
func lstatRegular(p string) (exists, regular bool, err error) {
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	return true, info.Mode().IsRegular(), nil
}
