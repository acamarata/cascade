// Purpose (this file): the pure, network-free file-tree helpers sync.go's
//
//	overwrite step and drift.go's comparison both need — reading a
//	directory's content into a comparable map, diffing two such maps, and
//	overwriting one directory's content from another (excluding `.git`).
//
// Inputs: local filesystem paths only.
// Outputs: sorted file lists (added/removed/changed), so a report reads
//
//	the same on every run regardless of directory-walk order.
//
// Constraints: never touches `.git` inside either tree — that directory is
//
//	git's own, and this package's git state changes go through GitRunner,
//	never direct filesystem writes. readTree REFUSES (typed
//	KindPolicyDenied) the instant it finds a symlink anywhere under root
//	(D4, confirming review finding 4): os.ReadFile follows a symlink to
//	wherever it points, and this package's own overwriteTree/Sync would
//	then read, commit and PUSH that target's content to the public GitHub
//	wiki — a local-file-read exfiltration path a symlink committed (by
//	accident or otherwise) under .github/wiki/ would open. Lstat, never
//	Stat, decides: Stat follows the link before this package ever sees
//	that it was one.
//
// SPORT: plugins/github/wiki:filetree (ADD) — P1-E25-W5-S51-T6.

package wiki

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// readTree reads every regular file under root (excluding a top-level
// `.git` directory) into a relative-path -> content map. It refuses the
// WHOLE read the instant any entry, at any depth, is a symlink (see this
// file's header comment) — never silently follows or silently skips one.
func readTree(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	// The root itself is Lstat'd too: a symlinked .github/wiki (or
	// --local-dir) would otherwise be followed silently and push content
	// from outside the tree (independent review, depth-0 case).
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, refuseSymlink(root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: reading %s", root)
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		full := filepath.Join(root, e.Name())
		info, err := os.Lstat(full)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: reading %s", full)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, refuseSymlink(full)
		}
		if info.IsDir() {
			sub, err := readTree(full)
			if err != nil {
				return nil, err
			}
			for rel, content := range sub {
				out[filepath.Join(e.Name(), rel)] = content
			}
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: reading %s", full)
		}
		out[e.Name()] = content
	}
	return out, nil
}

// diffTrees compares local against remote and reports, each sorted, the
// paths present only in local (added), present only in remote (removed),
// and present in both with different content (changed).
func diffTrees(local, remote map[string][]byte) (added, removed, changed []string) {
	for path, content := range local {
		rc, ok := remote[path]
		switch {
		case !ok:
			added = append(added, path)
		case !bytes.Equal(content, rc):
			changed = append(changed, path)
		}
	}
	for path := range remote {
		if _, ok := local[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}

// overwriteTree makes dstDir's content (excluding `.git`) equal srcDir's
// content: every file srcDir has is written into dstDir, and every file
// dstDir has that srcDir does not is removed. It reports the sorted union
// of paths it touched (added, changed or removed), for the caller's delta
// report.
func overwriteTree(srcDir, dstDir string) ([]string, error) {
	src, err := readTree(srcDir)
	if err != nil {
		return nil, err
	}
	dst, err := readTree(dstDir)
	if err != nil {
		return nil, err
	}
	added, removed, changed := diffTrees(src, dst)
	for _, rel := range append(append([]string{}, added...), changed...) {
		full := filepath.Join(dstDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: creating %s", filepath.Dir(full))
		}
		if err := os.WriteFile(full, src[rel], 0o644); err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: writing %s", full)
		}
	}
	for _, rel := range removed {
		if err := os.Remove(filepath.Join(dstDir, rel)); err != nil && !os.IsNotExist(err) {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-github wiki: removing %s", rel)
		}
	}
	touched := append(append(append([]string{}, added...), changed...), removed...)
	sort.Strings(touched)
	return touched, nil
}

// refuseSymlink is the single KindPolicyDenied refusal readTree answers for
// a symlink at any depth, the root included.
func refuseSymlink(path string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"cascade-github wiki: %s is a symlink; wiki content must be regular files and directories "+
			"only (a symlink could read or push content from outside .github/wiki/)", path)
}
