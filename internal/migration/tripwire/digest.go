// Package tripwire checks the committed migration fixtures against their reference.
// Inputs: fixture roots inside one source module. Outputs: byte and path digests.
// Constraints: no symlinks, escaping roots, empty sets, or checkout normalization.
// SPORT: migration golden-tripwire contract.
package tripwire

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Digest holds the canonical aggregate and each module-relative file checksum.
type Digest struct {
	SHA256 string
	Files  map[string]string
}

// ComputeDigest hashes sorted "<slash path>\t<sha256>\n" records. All roots
// must belong to the same source module; historical fixtures remain covered.
func ComputeDigest(roots []string) (Digest, error) {
	digest := Digest{Files: make(map[string]string)}
	if len(roots) == 0 {
		return digest, invalid("empty golden roots")
	}
	module, ok := ModuleRoot(roots[0])
	if !ok {
		return digest, invalid("no source module for %s", roots[0])
	}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return digest, invalid("root %s: %v", root, err)
		}
		if err := safeRoot(module, abs); err != nil {
			return digest, err
		}
		if err := collectFiles(module, abs, digest.Files); err != nil {
			return digest, err
		}
	}
	if len(digest.Files) == 0 {
		return digest, invalid("empty golden fixture set")
	}
	var paths []string
	for path := range digest.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(hash, "%s\t%s\n", path, digest.Files[path])
	}
	digest.SHA256 = fmt.Sprintf("%x", hash.Sum(nil))
	return digest, nil
}

// safeRoot rejects escapes and symlinks in every component below the module.
func safeRoot(module, root string) error {
	rel, err := filepath.Rel(module, root)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return invalid("golden root outside module: %s", root)
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return invalid("golden root %s: %v", dir, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return invalid("non-directory or symlink root: %s", dir)
		}
		if dir == module {
			return nil
		}
	}
}

// collectFiles reads only regular files, retaining paths so additions and renames count.
func collectFiles(module, root string, files map[string]string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return invalid("golden file %s: %v", path, err)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return invalid("symlink golden file: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return invalid("non-regular golden file: %s", path)
		}
		rel, err := filepath.Rel(module, path)
		if err != nil {
			return invalid("golden path %s: %v", path, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == referencePath {
			return nil
		}
		if strings.ContainsAny(rel, "\t\r\n\\") {
			return invalid("unsupported golden path: %q", rel)
		}
		data, err := os.ReadFile(path) //nolint:gosec // validated module-contained fixture walk
		if err != nil {
			return invalid("golden file %s: %v", rel, err)
		}
		files[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
}

// invalid gives every refusal the existing invalid-input taxonomy kind.
func invalid(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "golden tripwire: "+format, args...)
}
