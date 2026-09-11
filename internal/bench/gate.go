// Package bench (this file) parses the text `go test -bench=. -benchmem` produces across
// however many packages a single invocation covers, qualify each
// benchmark's name with its owning import path, and run the result
// through internal/build.AssertBudgets against the Budgets table.
//
// Inputs: the raw stdout of a bench run, as an io.Reader. The format is
// go's own: a package boundary is announced by an "ok  \t<import
// path>\t..." (or "?   \t<import path>\t[no test files]") line, and each
// benchmark result is a line starting "Benchmark" followed by
// tab-or-space-separated ns/op, B/op and allocs/op fields.
//
// Outputs: ParseBenchOutput returns one build.BudgetResult per benchmark
// line, Name already qualified as "<import path>/<BenchmarkName>". Gate
// returns the violations AssertBudgets finds, or nil on a clean run.
//
// Constraints: a benchmark line seen before any "ok"/"?" package line
// (malformed input, or a caller feeding a single-package run without its
// header) is refused with a typed error rather than silently qualified
// with an empty import path, which would collide two same-named
// benchmarks in different packages into one budget key.
//
// SPORT: internal.bench.Gate/ADDED (P1-E28-W10-S58-T2).
package bench

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/pkg/cascade"
)

// pkgLineRE matches the package-boundary lines `go test -bench` emits.
// Three shapes carry the import path: the "pkg: <path>" header printed
// BEFORE that package's benchmark lines, and the "ok  \t<path>\t0.5s" /
// "?   \t<path>\t[no test files]" trailer printed after. Matching all
// three means a benchmark line is qualified correctly whether the
// transcript includes the pre-run header (the default `go test -bench`
// shape) or only the post-run trailer (a caller-constructed transcript
// that omits it).
var pkgLineRE = regexp.MustCompile(`^(?:pkg:\s+(\S+)|(?:ok|\?)\s+(\S+)\s)`)

// benchLineRE matches one benchmark result line. Go's own field order is
// fixed: name, iterations, ns/op, then optional B/op and allocs/op pairs
// depending on -benchmem.
var benchLineRE = regexp.MustCompile(
	`^(Benchmark\S+)\s+\d+\s+([\d.]+)\s+ns/op(?:\s+([\d.]+)\s+B/op)?(?:\s+([\d.]+)\s+allocs/op)?`)

// ParseBenchOutput reads a `go test -bench` transcript and returns one
// BudgetResult per benchmark line, its Name qualified as
// "<import path>/<BenchmarkName>". Refuses with cascade.KindInvalidInput
// if a benchmark line appears before any package-boundary line has been
// seen.
func ParseBenchOutput(r io.Reader) ([]build.BudgetResult, error) {
	var (
		out        []build.BudgetResult
		currentPkg string
	)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if m := pkgLineRE.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				currentPkg = m[1]
			} else {
				currentPkg = m[2]
			}
			continue
		}
		m := benchLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if currentPkg == "" {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"bench: benchmark line %q seen before any package-boundary line", m[1])
		}
		result, err := parseBenchFields(currentPkg, m)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	if err := scanner.Err(); err != nil {
		return nil, cascade.Newf(cascade.KindInvalidInput, "bench: reading bench output: %v", err)
	}
	return out, nil
}

// parseBenchFields converts one benchLineRE match into a qualified
// BudgetResult. B/op and allocs/op are optional (a run without
// -benchmem omits them); their absence yields a zero value, which
// AssertBudgets already treats as "no ceiling on that metric" so a
// missing measurement never fabricates a pass or a fail.
func parseBenchFields(pkgPath string, m []string) (build.BudgetResult, error) {
	nsPerOp, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return build.BudgetResult{}, cascade.Newf(cascade.KindInvalidInput,
			"bench: benchmark %q: invalid ns/op %q: %v", m[1], m[2], err)
	}
	var bytesPerOp, allocsPerOp int64
	if m[3] != "" {
		v, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			return build.BudgetResult{}, cascade.Newf(cascade.KindInvalidInput,
				"bench: benchmark %q: invalid B/op %q: %v", m[1], m[3], err)
		}
		bytesPerOp = int64(v)
	}
	if m[4] != "" {
		v, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return build.BudgetResult{}, cascade.Newf(cascade.KindInvalidInput,
				"bench: benchmark %q: invalid allocs/op %q: %v", m[1], m[4], err)
		}
		allocsPerOp = int64(v)
	}
	return build.BudgetResult{
		Name:        pkgPath + "/" + m[1],
		NsPerOp:     nsPerOp,
		AllocsPerOp: allocsPerOp,
		BytesPerOp:  bytesPerOp,
	}, nil
}

// Gate runs results against Budgets and returns every violation found —
// nil on a clean run. This is the one call bench.yml's budget-assertion
// step makes; a non-nil return is the CI gate's non-zero exit.
func Gate(results []build.BudgetResult) []build.BudgetViolation {
	return build.AssertBudgets(results, Budgets)
}
