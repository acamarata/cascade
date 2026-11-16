package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxInputFileBytes is the maximum allowed size for an input file.
const MaxInputFileBytes int64 = 1 << 20

// ErrUnsafeInputFile is returned when an input file fails safety checks.
var ErrUnsafeInputFile = errors.New("unsafe input file")

// CheckInputFile validates that path is safe to read as an input file.
// It rejects empty or whitespace-only paths, symlinks in any path component,
// and paths not contained within one of the publicRoots.
func CheckInputFile(path string, publicRoots []string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafeInputFile)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeInputFile, err)
	}
	cleanPath := filepath.Clean(absPath)

	if err := checkNoSymlink(cleanPath); err != nil {
		return err
	}

	roots, err := validatePublicRoots(publicRoots)
	if err != nil {
		return err
	}

	if !isUnderAnyRoot(cleanPath, roots) {
		return fmt.Errorf("%w: path not under any public root", ErrUnsafeInputFile)
	}

	return nil
}

func checkNoSymlink(cleanPath string) error {
	parts := splitPath(cleanPath)
	current := string(filepath.Separator)
	for _, p := range parts {
		current = filepath.Join(current, p)
		fi, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: lstat %s: %v", ErrUnsafeInputFile, current, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink detected at %s", ErrUnsafeInputFile, current)
		}
	}
	return nil
}

func splitPath(p string) []string {
	var parts []string
	for {
		dir, base := filepath.Split(p)
		if base != "" {
			parts = append([]string{base}, parts...)
		}
		dir = filepath.Clean(dir)
		if dir == p || dir == "." || dir == string(filepath.Separator) {
			break
		}
		p = dir
	}
	return parts
}

func validatePublicRoots(publicRoots []string) ([]string, error) {
	seen := make(map[string]bool)
	var roots []string
	for _, r := range publicRoots {
		if strings.TrimSpace(r) == "" {
			return nil, fmt.Errorf("%w: blank public root", ErrUnsafeInputFile)
		}
		if !filepath.IsAbs(r) {
			return nil, fmt.Errorf("%w: relative public root", ErrUnsafeInputFile)
		}
		cr := filepath.Clean(r)
		if seen[cr] {
			return nil, fmt.Errorf("%w: duplicate public root", ErrUnsafeInputFile)
		}
		seen[cr] = true

		fi, err := os.Lstat(cr)
		if err != nil {
			return nil, fmt.Errorf("%w: unreadable root: %v", ErrUnsafeInputFile, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: root is a symlink", ErrUnsafeInputFile)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%w: root is not a directory", ErrUnsafeInputFile)
		}
		roots = append(roots, cr)
	}
	return roots, nil
}

func isUnderAnyRoot(cleanPath string, roots []string) bool {
	for _, r := range roots {
		rel, err := filepath.Rel(r, cleanPath)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && rel != "..") {
			return true
		}
	}
	return false
}
