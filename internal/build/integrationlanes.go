// Purpose: the integration-lane coverage gate. DiscoverIntegrationPackages
// finds every package that has a test file whose //go:build expression is
// satisfiable only with the integration tag (parsed with go/build/constraint,
// never a substring match); CheckLaneCoverage proves each is a registry row or
// a reasoned exclusion, that every row's CI job exists, blocks merges and may
// not fail softly, and that the expected-to-fail red-proof jobs keep their
// shape (R18 M7). Inputs: the module tree and .github/workflows/ci.yml bytes.

package build

import (
	"bufio"
	"fmt"
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxFreeTags bounds the truth-table search over the non-platform tags.
const maxFreeTags = 20

// DiscoverIntegrationPackages lists, as sorted "./rel/dir" paths, the packages
// under moduleRoot with a _test.go file whose build expression is false
// without the integration tag and true with it. testdata, vendor, dot and
// underscore directories and nested modules are skipped.
func DiscoverIntegrationPackages(moduleRoot string) ([]string, error) {
	found := map[string]bool{}
	err := filepath.WalkDir(moduleRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return skipDir(moduleRoot, p, d.Name())
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		need, err := fileRequiresIntegration(p)
		if err != nil {
			return err
		} else if need {
			rel, _ := filepath.Rel(moduleRoot, filepath.Dir(p))
			found["./"+filepath.ToSlash(rel)] = true
		}
		return nil
	})
	out := make([]string, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, err
}

// skipDir returns fs.SkipDir for directories the go tool never builds.
func skipDir(root, p, name string) error {
	if p == root {
		return nil
	}
	if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return fs.SkipDir
	}
	if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
		return fs.SkipDir
	}
	return nil
}

// fileRequiresIntegration reports whether the file's //go:build line needs the
// integration tag.
func fileRequiresIntegration(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return false, nil
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		return RequiresTag(expr, "integration")
	}
	return false, sc.Err()
}

// RequiresTag reports whether expr is unsatisfiable without tag and
// satisfiable with it. GOOS tags are mutually exclusive, unix follows GOOS,
// amd64 and go1.N are true, and every other tag is free.
func RequiresTag(expr constraint.Expr, tag string) (bool, error) {
	without, err := satisfiable(expr, tag, false)
	if err != nil || without {
		return false, err
	}
	return satisfiable(expr, tag, true)
}

// satisfiable searches the platform x free-tag truth table for an assignment
// making expr true with tag set to value.
func satisfiable(expr constraint.Expr, tag string, value bool) (bool, error) {
	names := map[string]bool{}
	collectTags(expr, names)
	var free []string
	for n := range names {
		if n != tag && !pinnedTag(n) {
			free = append(free, n)
		}
	}
	if len(free) > maxFreeTags {
		return false, fmt.Errorf("build expression has %d free tags, more than %d", len(free), maxFreeTags)
	}
	bit := map[string]int{}
	for i, n := range free {
		bit[n] = i
	}
	for _, goos := range []string{"linux", "darwin", "windows", "plan9"} {
		for mask := 0; mask < 1<<len(free); mask++ {
			eval := func(t string) bool {
				switch {
				case t == tag:
					return value
				case t == goos:
					return true
				case t == "unix":
					return goos == "linux" || goos == "darwin"
				case pinnedTag(t):
					return t == "amd64" || strings.HasPrefix(t, "go1.")
				}
				return mask>>bit[t]&1 == 1
			}
			if expr.Eval(eval) {
				return true, nil
			}
		}
	}
	return false, nil
}

// pinnedTag reports whether the platform model fixes the tag.
func pinnedTag(t string) bool {
	switch t {
	case "linux", "darwin", "windows", "plan9", "unix", "amd64":
		return true
	}
	return strings.HasPrefix(t, "go1.")
}

// collectTags gathers every tag name in expr.
func collectTags(expr constraint.Expr, into map[string]bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		into[e.Tag] = true
	case *constraint.NotExpr:
		collectTags(e.X, into)
	case *constraint.AndExpr:
		collectTags(e.X, into)
		collectTags(e.Y, into)
	case *constraint.OrExpr:
		collectTags(e.X, into)
		collectTags(e.Y, into)
	}
}

// CheckLaneCoverage returns every way the registry, the exclusions and ci.yml
// disagree about pkgs: an unmapped, stale or doubly-listed package, an invalid
// row, a row whose job is absent, soft-failing or not required by ci-gate (or
// whose step does not run the package with the tags), and a broken red-proof
// shape. An empty result means the lanes cover the tree.
func CheckLaneCoverage(pkgs []string, rows []LaneRow, excluded map[string]string, ciYAML []byte) []string {
	var wf ciWorkflow
	if err := yaml.Unmarshal(ciYAML, &wf); err != nil {
		return []string{"ci.yml does not parse: " + err.Error()}
	}
	problems := coverageProblems(pkgs, rows, excluded)
	for _, r := range rows {
		problems = append(problems, rowProblems(r, wf)...)
	}
	problems = append(problems, laneOverlaps(rows)...)
	problems = append(problems, redProofProblems(wf)...)
	sort.Strings(problems)
	return slices.Compact(problems)
}

// coverageProblems checks the package sets against the registry.
func coverageProblems(pkgs []string, rows []LaneRow, excluded map[string]string) []string {
	var out []string
	rowPkgs := map[string]bool{}
	for _, r := range rows {
		rowPkgs[r.Package] = true
	}
	for _, p := range pkgs {
		_, ex := excluded[p]
		if !rowPkgs[p] && !ex {
			out = append(out, fmt.Sprintf("unmapped integration package %s: add a row to IntegrationLanes or a reasoned IntegrationExcluded entry", p))
		}
		if rowPkgs[p] && ex {
			out = append(out, p+" is both an integration row and excluded")
		}
	}
	for p := range rowPkgs {
		if !slices.Contains(pkgs, p) {
			out = append(out, "stale integration row: "+p+" has no integration-constrained test file")
		}
	}
	for p, reason := range excluded {
		if !slices.Contains(pkgs, p) {
			out = append(out, "stale integration exclusion: "+p+" has no integration-constrained test file")
		}
		if strings.TrimSpace(reason) == "" {
			out = append(out, "integration exclusion "+p+" has no reason")
		}
	}
	return out
}

// laneOverlaps flags two lane rows that run one package on one runner, which
// the report could not tell apart.
func laneOverlaps(rows []LaneRow) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Job != LaneJob {
			continue
		}
		for _, os := range r.OS {
			if k := r.Package + "@" + os; seen[k] {
				out = append(out, fmt.Sprintf("integration lane has two rows for %s on %s", r.Package, os))
			} else {
				seen[k] = true
			}
		}
	}
	return out
}

// rowProblems validates one row on its own and against its CI job.
func rowProblems(r LaneRow, wf ciWorkflow) []string {
	var out []string
	bad := func(format string, a ...any) {
		out = append(out, fmt.Sprintf("row %s: "+format, append([]any{r.Package}, a...)...))
	}
	if !slices.Contains(strings.Fields(r.Tags), "integration") {
		bad("tags %q lack integration", r.Tags)
	}
	if len(r.OS) == 0 {
		bad("lists no runner")
	}
	for _, os := range r.OS {
		if !slices.Contains(allRunners, os) {
			bad("unknown runner %q", os)
		}
	}
	for os, msg := range r.SkipOn {
		if !slices.Contains(r.OS, os) || strings.TrimSpace(msg) == "" {
			bad("SkipOn %q needs a listed runner and a gate message", os)
		}
	}
	if r.Job == LaneJob {
		if _, err := regexp.Compile(r.RunRegex); err != nil || r.RunRegex == "" {
			bad("run regex %q is empty or invalid", r.RunRegex)
		}
	}
	return append(out, jobProblems(r, wf)...)
}

// jobProblems checks the row's job exists, cannot fail softly, is required by
// ci-gate, and (for a job other than the lane) runs the package with the tags.
func jobProblems(r LaneRow, wf ciWorkflow) []string {
	job, ok := wf.Jobs[r.Job]
	if !ok {
		return []string{fmt.Sprintf("row %s: job %q is absent from ci.yml", r.Package, r.Job)}
	}
	var out []string
	if softFail(job) {
		out = append(out, fmt.Sprintf("row %s: job %q has continue-on-error, so a failure would not block", r.Package, r.Job))
	}
	if !slices.Contains(wf.Jobs[CIGateJob].Needs, r.Job) {
		out = append(out, fmt.Sprintf("row %s: job %q is not in ci-gate needs", r.Package, r.Job))
	}
	if r.Job != LaneJob && !runsPackage(job, r) {
		out = append(out, fmt.Sprintf("row %s: job %q has no step running it with -tags %q", r.Package, r.Job, r.Tags))
	}
	return out
}
