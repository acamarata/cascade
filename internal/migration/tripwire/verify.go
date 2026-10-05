package tripwire

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const referencePath = "internal/migration/testdata/golden-checksums.sha256"

var goldenDirs = []string{
	"internal/memory/testdata/v1-goldens/migration",
	"internal/secrets/testdata/v1-goldens/migration",
	"internal/providers/registry/testdata/v1-goldens/migration",
	"internal/runtime/testdata/v1-goldens/migration",
}

// Report names every added, missing or changed file in sorted module-relative order.
type Report struct{ Paths []string }

// VerifyTripwire compares current bytes with a nonempty committed reference.
// Inputs: source module and absolute or module-relative reference filename.
// Outputs: named differences and an error. Constraints: warning callers ignore the
// error for their exit status; no timestamp threshold, pruning or silent omissions.
// SPORT: migration golden-tripwire contract.
func VerifyTripwire(moduleRoot, referenceFile string) (Report, error) {
	var report Report
	if !filepath.IsAbs(referenceFile) {
		referenceFile = filepath.Join(moduleRoot, referenceFile)
	}
	reference, err := readReference(referenceFile)
	if err != nil {
		report.Paths = []string{referenceFile}
		return report, err
	}
	var roots []string
	for _, dir := range goldenDirs {
		roots = append(roots, filepath.Join(moduleRoot, filepath.FromSlash(dir)))
	}
	digest, err := ComputeDigest(roots)
	if err != nil {
		return report, err
	}
	for path, sum := range reference {
		if digest.Files[path] != sum {
			report.Paths = append(report.Paths, path)
		}
	}
	for path := range digest.Files {
		if _, ok := reference[path]; !ok {
			report.Paths = append(report.Paths, path)
		}
	}
	sort.Strings(report.Paths)
	if len(report.Paths) != 0 {
		return report, invalid("stale checksums: %s", strings.Join(report.Paths, ", "))
	}
	return report, nil
}

// readReference refuses malformed, repeated and unsorted rows, including escapes.
func readReference(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller's reference file
	if err != nil {
		return nil, invalid("reference %s: %v", path, err)
	}
	files := make(map[string]string)
	previous := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		sum, rel, ok := strings.Cut(line, "  ")
		decoded, decodeErr := hex.DecodeString(sum)
		if !ok || decodeErr != nil || len(decoded) != 32 || sum != strings.ToLower(sum) || !validReferencePath(rel) || rel <= previous {
			return nil, invalid("malformed reference %s row %q", path, line)
		}
		files[rel], previous = sum, rel
	}
	if len(files) == 0 {
		return nil, invalid("empty checksum set in %s", path)
	}
	return files, nil
}

// validReferencePath limits reference rows to the four fixture directories.
func validReferencePath(path string) bool {
	if strings.ContainsAny(path, "\\\t\r\n") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path {
		return false
	}
	for _, dir := range goldenDirs {
		if strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}
