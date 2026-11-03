package docgen

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DriftKind says how one page differs from disk.
type DriftKind string

// The three ways a tree can drift.
const (
	// DriftChanged: the page exists with different bytes.
	DriftChanged DriftKind = "changed"
	// DriftMissing: the page is not on disk.
	DriftMissing DriftKind = "missing"
	// DriftExtra: an owned page is on disk that no longer renders.
	DriftExtra DriftKind = "extra"
)

// Drift is one difference between the rendered pages and the directory.
type Drift struct {
	Path string
	Kind DriftKind
}

// readFile is the read seam; only _test.go replaces it.
var readFile = os.ReadFile

// DiffTree compares pages with the files under dir. A read error other than
// not-exist is returned as an error, never reported as missing.
func DiffTree(dir string, pages []Page) ([]Drift, error) {
	if err := checkPages(pages); err != nil {
		return nil, err
	}
	var out []Drift
	want := map[string]bool{}
	for _, p := range pages {
		want[p.Path] = true
		got, err := readFile(filepath.Join(dir, filepath.FromSlash(p.Path)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out = append(out, Drift{p.Path, DriftMissing})
		case err != nil:
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "docgen: read %s", p.Path)
		case !bytes.Equal(got, p.Body):
			out = append(out, Drift{p.Path, DriftChanged})
		}
	}
	extras, err := ownedExtras(dir, want)
	if err != nil {
		return nil, err
	}
	for _, e := range extras {
		out = append(out, Drift{e, DriftExtra})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// WriteTree publishes pages under dir atomically and removes owned pages
// that no longer render. A page path held by a file without OwnedMarker is a
// KindConflict, reported before anything is written.
func WriteTree(dir string, pages []Page) error {
	if err := checkPages(pages); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, p := range pages {
		want[p.Path] = true
		got, err := readFile(filepath.Join(dir, filepath.FromSlash(p.Path)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return cascade.Wrapf(cascade.KindUnavailable, err, "docgen: read %s", p.Path)
		case !isOwned(got):
			return cascade.Newf(cascade.KindConflict, "docgen: %s exists and is not generator-owned", p.Path)
		}
	}
	extras, err := ownedExtras(dir, want)
	if err != nil {
		return err
	}
	for _, p := range pages {
		full := filepath.Join(dir, filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "docgen: mkdir for %s", p.Path)
		}
		if err := runtime.WriteFileAtomic(full, p.Body, 0o644); err != nil {
			return err
		}
	}
	for _, e := range extras {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(e))); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "docgen: remove %s", e)
		}
	}
	return nil
}

func isOwned(b []byte) bool {
	return bytes.HasPrefix(b, []byte(OwnedMarker))
}

// ownedExtras lists owned files under dir whose relative path is not wanted.
func ownedExtras(dir string, want map[string]bool) ([]string, error) {
	var extras []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == dir {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if want[rel] {
			return nil
		}
		b, err := readFile(p)
		if err != nil {
			return err
		}
		if isOwned(b) {
			extras = append(extras, rel)
		}
		return nil
	})
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "docgen: scan %s", dir)
	}
	sort.Strings(extras)
	return extras, nil
}

func checkPages(pages []Page) error {
	seen := map[string]bool{}
	for _, p := range pages {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p.Path)))
		if p.Path == "" || clean != p.Path || filepath.IsAbs(p.Path) || strings.HasPrefix(clean, "../") || clean == ".." {
			return cascade.Newf(cascade.KindInvalidInput, "docgen: bad page path %q", p.Path)
		}
		if seen[p.Path] {
			return cascade.Newf(cascade.KindInvalidInput, "docgen: duplicate page %q", p.Path)
		}
		seen[p.Path] = true
	}
	return nil
}
