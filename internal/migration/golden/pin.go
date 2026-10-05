// Purpose: digest-pin the committed v1 importer input set before any byte of
// it is staged. INPUTS.sha256 is the only enumeration of what may be read.
// Inputs: the pinned dir internal/migration/v1/testdata/v1-goldens and its
// INPUTS.sha256 manifest ("<sha256>  <relpath>" lines, sorted, LF only).
// Outputs: the verified input files, each carrying the exact bytes that were
// hashed, so the staged copy can never differ from the digest-checked copy.
// Constraints: fail closed. An unlisted file, a digest mismatch, a symlink, a
// path escaping the pinned dir after filepath.EvalSymlinks, a malformed or
// unsorted manifest, or a pinned dir that is itself a link each refuse the
// whole run before anything is written. Messages name relative paths only.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

// manifestName is the digest manifest file inside the pinned dir.
const manifestName = "INPUTS.sha256"

// inputDomains are the four pinned input dirs, in harvest order.
var inputDomains = []v1.Domain{v1.DomainMemory, v1.DomainVault, v1.DomainAccounts, v1.DomainConfig}

// inputFile is one verified input: its domain, its pinned-dir-relative
// slash path, its sha256 and the exact bytes that produced that digest.
type inputFile struct {
	Domain v1.Domain
	Rel    string
	Digest string
	Data   []byte
}

// manifestEntry is one parsed INPUTS.sha256 line.
type manifestEntry struct {
	digest string
	rel    string
}

// refusal builds the pinning refusal every check in this file returns.
func refusal(format string, args ...any) error {
	return cascade.Newf(cascade.KindIntegrity, "golden harvest: refused input: "+format, args...)
}

// loadPinned verifies the whole pinned dir against its manifest and returns
// every listed file's bytes. Nothing is returned unless every check passes.
func loadPinned(pinnedDir string) ([]inputFile, error) {
	info, err := os.Lstat(pinnedDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, refusal("the pinned input dir is missing or is not a real directory")
	}
	realRoot, err := filepath.EvalSymlinks(pinnedDir)
	if err != nil {
		return nil, refusal("the pinned input dir cannot be resolved")
	}
	entries, err := readManifest(pinnedDir)
	if err != nil {
		return nil, err
	}
	listed := make(map[string]bool, len(entries))
	for _, entry := range entries {
		listed[entry.rel] = true
	}
	if err := refuseUnlisted(pinnedDir, listed); err != nil {
		return nil, err
	}
	out := make([]inputFile, 0, len(entries))
	for _, entry := range entries {
		file, err := readPinnedFile(pinnedDir, realRoot, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, nil
}

// readManifest reads and strictly parses INPUTS.sha256.
func readManifest(pinnedDir string) ([]manifestEntry, error) {
	manifestPath := filepath.Join(pinnedDir, manifestName)
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, refusal("%s is missing or is not a regular file", manifestName)
	}
	data, err := os.ReadFile(manifestPath) //nolint:gosec // fixed manifest inside the pinned dir
	if err != nil {
		return nil, refusal("%s cannot be read", manifestName)
	}
	return parseManifest(string(data))
}

// parseManifest parses manifest text: one "<64 hex>  <relpath>" per LF-ended
// line, strictly ascending by path, at least one line, nothing else.
func parseManifest(text string) ([]manifestEntry, error) {
	if text == "" || !strings.HasSuffix(text, "\n") || strings.Contains(text, "\r") {
		return nil, refusal("%s must be non-empty LF-terminated lines", manifestName)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	out := make([]manifestEntry, 0, len(lines))
	for i, line := range lines {
		digest, rel, ok := strings.Cut(line, "  ")
		if !ok || !isSHA256Hex(digest) {
			return nil, refusal("%s line %d is not \"<sha256>  <path>\"", manifestName, i+1)
		}
		if err := checkManifestPath(rel); err != nil {
			return nil, refusal("%s line %d: %v", manifestName, i+1, err)
		}
		if len(out) > 0 && out[len(out)-1].rel >= rel {
			return nil, refusal("%s line %d is unsorted or duplicated", manifestName, i+1)
		}
		out = append(out, manifestEntry{digest: digest, rel: rel})
	}
	return out, nil
}

// isSHA256Hex reports whether s is exactly 64 lowercase hex digits.
func isSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkManifestPath refuses any listed path that is not a clean, relative,
// slash-separated path inside one of the four input dirs.
func checkManifestPath(rel string) error {
	switch {
	case rel == "" || strings.ContainsAny(rel, "\\\x00") || path.IsAbs(rel) || filepath.IsAbs(rel):
		return cascade.New(cascade.KindInvalidInput, "path is empty, absolute or has a forbidden byte")
	case path.Clean(rel) != rel || strings.Contains("/"+rel+"/", "/../"):
		return cascade.New(cascade.KindInvalidInput, "path is not clean or climbs out of the pinned dir")
	case domainOf(rel) == "" || !strings.Contains(rel, "/"):
		return cascade.New(cascade.KindInvalidInput, "path is outside the four input dirs")
	case path.Base(rel) == "README.md":
		return cascade.New(cascade.KindInvalidInput, "README.md is provenance, never an input")
	}
	return nil
}

// domainOf returns the input domain owning rel, or "" when none does.
func domainOf(rel string) v1.Domain {
	first, _, _ := strings.Cut(rel, "/")
	for _, domain := range inputDomains {
		if first == string(domain) {
			return domain
		}
	}
	return ""
}

// refuseUnlisted walks the four input dirs and refuses a missing dir, any
// link or special file, and any regular file (README.md aside) that the
// manifest does not list.
func refuseUnlisted(pinnedDir string, listed map[string]bool) error {
	for _, domain := range inputDomains {
		dir := filepath.Join(pinnedDir, string(domain))
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return refusal("pinned input dir %s is absent or is not a real directory", domain)
		}
		walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return refusal("pinned input dir %s cannot be walked", domain)
			}
			return checkWalkedEntry(pinnedDir, p, d, listed)
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// checkWalkedEntry classifies one entry found under an input dir.
func checkWalkedEntry(pinnedDir, p string, d fs.DirEntry, listed map[string]bool) error {
	rel, err := filepath.Rel(pinnedDir, p)
	if err != nil {
		return refusal("an input path cannot be made relative")
	}
	rel = filepath.ToSlash(rel)
	switch {
	case d.Type()&fs.ModeSymlink != 0:
		return refusal("%s is a symlink", rel)
	case d.IsDir():
		return nil
	case !d.Type().IsRegular():
		return refusal("%s is not a regular file", rel)
	case d.Name() == "README.md":
		return nil
	case !listed[rel]:
		return refusal("%s is not listed in %s", rel, manifestName)
	}
	return nil
}

// readPinnedFile reads one listed file, proves it is a regular file that
// resolves inside the pinned dir, and checks its digest over the exact bytes
// returned.
func readPinnedFile(pinnedDir, realRoot string, entry manifestEntry) (inputFile, error) {
	p := filepath.Join(pinnedDir, filepath.FromSlash(entry.rel))
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return inputFile{}, refusal("%s is listed but missing or not a regular file", entry.rel)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || !within(realRoot, resolved) {
		return inputFile{}, refusal("%s resolves outside the pinned dir", entry.rel)
	}
	data, err := os.ReadFile(resolved) //nolint:gosec // resolved inside the pinned dir above
	if err != nil {
		return inputFile{}, refusal("%s cannot be read", entry.rel)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != entry.digest {
		return inputFile{}, refusal("%s does not match its %s digest", entry.rel, manifestName)
	}
	return inputFile{Domain: domainOf(entry.rel), Rel: entry.rel, Digest: entry.digest, Data: data}, nil
}

// within reports whether target is root or lies beneath it.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
