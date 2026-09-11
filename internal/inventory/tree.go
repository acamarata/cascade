// Purpose: the Report fields only computable by walking the checked-out
//
//	tree — Providers, Plugins, SPORTLines, Platforms. An installed binary
//	has none of providers/, plugins/, or .github/workflows/ on disk, so
//	these are computed once, at generation time (internal/inventory/gen),
//	against a real checkout, and the result is baked into the tracked
//	counts.json artifact this package embeds (embed.go). The internal/build
//	drift gate re-runs these same functions against the live tree and
//	fails if the tracked artifact disagrees (that comparison is the gate;
//	this file only supplies the "what does the tree say right now" half).
//
// Inputs: repoRoot, the checkout's absolute path (the generator resolves it
//
//	via `git rev-parse --show-toplevel`; the drift gate test already has it
//	from its own harness).
//
// Outputs: TreeCounts, or an error when a required path is missing.
// Constraints: fails closed — a missing providers/, plugins/, or
//
//	ci.yml is a hard error, never a silent zero (a silent zero would read
//	as "there are no providers", which is worse than refusing to answer).
//
// SPORT: internal.inventory.ComputeTreeCounts/ADDED,
//
//	internal.inventory.PlatformsFromCI/ADDED.

package inventory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// TreeCounts holds the tree-derived half of Report.
type TreeCounts struct {
	Providers  int
	Plugins    int
	SPORTLines int
	Platforms  []string
}

// ComputeTreeCounts walks repoRoot's providers/ and plugins/ directories
// and every tracked .go file, and returns the counts derived from what it
// finds. It does not read counts.json and does not know about the drift
// gate; it only answers "what does the tree say right now".
func ComputeTreeCounts(repoRoot string) (TreeCounts, error) {
	providers, err := countTopLevelDirs(filepath.Join(repoRoot, "providers"))
	if err != nil {
		return TreeCounts{}, fmt.Errorf("inventory: providers/: %w", err)
	}
	plugins, err := countTopLevelDirs(filepath.Join(repoRoot, "plugins"))
	if err != nil {
		return TreeCounts{}, fmt.Errorf("inventory: plugins/: %w", err)
	}
	sportLines, err := countSPORTLines(repoRoot)
	if err != nil {
		return TreeCounts{}, fmt.Errorf("inventory: sport lines: %w", err)
	}
	platforms, err := PlatformsFromCI(repoRoot)
	if err != nil {
		return TreeCounts{}, fmt.Errorf("inventory: platforms: %w", err)
	}
	return TreeCounts{
		Providers:  providers,
		Plugins:    plugins,
		SPORTLines: sportLines,
		Platforms:  platforms,
	}, nil
}

// countTopLevelDirs returns how many directory entries dir has, one level
// deep, ignoring plain files (a stray README at that level would otherwise
// inflate the count).
func countTopLevelDirs(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n, nil
}

// countSPORTLines counts every line beginning (after leading whitespace and
// the "//" comment marker) with "SPORT:" across repoRoot's git-tracked .go
// files. It is a raw line count, not a deduplicated entity registry: a
// SPORT comment that lists several dotted paths on one marker line counts
// once; see doc.go for what this does and does not prove.
func countSPORTLines(repoRoot string) (int, error) {
	files, err := gitTrackedGoFiles(repoRoot)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			if os.IsNotExist(err) {
				// A concurrently-editing agent's uncommitted working-tree
				// deletion of a still-index-tracked path. Skipping (rather
				// than failing closed, this file's usual rule) is the only
				// sane response on a multi-agent tree: the file will
				// either be restored or its deletion committed before this
				// runs again, and neither outcome is this function's to
				// adjudicate.
				continue
			}
			return 0, fmt.Errorf("read %s: %w", rel, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
			if strings.HasPrefix(strings.TrimSpace(trimmed), "SPORT:") {
				total++
			}
		}
	}
	return total, nil
}

// gitTrackedGoFiles returns every git-tracked *.go path under repoRoot,
// repo-relative. Duplicated (rather than imported) from internal/build's
// ListTrackedFiles: this package is linked into the production CLI binary
// (via cmd/cascade's doctor counts view), and internal/build is a
// test/gate-oriented package this tree keeps out of production binaries.
func gitTrackedGoFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f == "" || isSeededViolationFixture(f) {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// seededViolationDir holds the deliberately-broken fixtures the gates in
// internal/build use to prove they can turn red.
const seededViolationDir = "internal/build/testdata/seeded-violations/"

// isSeededViolationFixture reports whether path is one of those fixtures.
//
// They must be excluded from every REAL-TREE scan, and the reason is not
// tidiness. The SPORT gate's own fixture contains a deliberately malformed
// SPORT marker so that the gate can be proven to fail on one. Scanning it
// as if it were ordinary source makes the real-tree check report a
// malformed line that is doing exactly its job, so the gate fails on its
// own evidence and the registry cannot be generated at all.
//
// This bit locally for an unusually sharp reason: the scan reads `git
// ls-files`, and the fixture was still UNTRACKED when the gate was written
// and verified. It became tracked in the same commit that shipped the gate,
// so the check went red the first time CI ran it and green every time
// locally beforehand. A gate verified against a working tree is not
// verified against the tree CI sees.
func isSeededViolationFixture(path string) bool {
	return strings.HasPrefix(path, seededViolationDir)
}

// ciMatrix mirrors the ".github/workflows/ci.yml" fields this file reads:
// only the build-test job's strategy.matrix.include list, ignoring every
// other key the real workflow carries.
type ciMatrix struct {
	Jobs map[string]struct {
		Strategy struct {
			Matrix struct {
				Include []struct {
					GOOS string `yaml:"goos"`
				} `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
	} `yaml:"jobs"`
}

// PlatformsFromCI parses repoRoot/.github/workflows/ci.yml and returns the
// deduplicated, sorted GOOS list every matrix.include entry declares across
// every job — the single declared source this repo's own CI runs from, so
// a supported-platform count can never disagree with what CI actually
// builds and tests.
func PlatformsFromCI(repoRoot string) ([]string, error) {
	path := filepath.Join(repoRoot, ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m ciMatrix
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, job := range m.Jobs {
		for _, entry := range job.Strategy.Matrix.Include {
			if entry.GOOS != "" {
				seen[entry.GOOS] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%s: no matrix.include[].goos entries found", path)
	}
	platforms := make([]string, 0, len(seen))
	for goos := range seen {
		platforms = append(platforms, goos)
	}
	sort.Strings(platforms)
	return platforms, nil
}
