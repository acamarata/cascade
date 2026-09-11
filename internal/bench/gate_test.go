// SPORT: internal.bench.ParseBenchOutput/ADDED test coverage,
//
//	internal.bench.Gate/ADDED test coverage (P1-E28-W10-S58-T2).
package bench

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/pkg/cascade"
)

const sampleTranscript = `goos: darwin
goarch: arm64
pkg: github.com/acamarata/cascade/providers/sqlite
BenchmarkSQLite-8   	 1000000	       123.4 ns/op	      45.0 B/op	       2 allocs/op
PASS
ok  	github.com/acamarata/cascade/providers/sqlite	1.234s
goos: darwin
goarch: arm64
pkg: github.com/acamarata/cascade/internal/fleet
BenchmarkHeadroomPublish-8   	  500000	       246.8 ns/op	      90.0 B/op	       4 allocs/op
PASS
ok  	github.com/acamarata/cascade/internal/fleet	2.468s
?   	github.com/acamarata/cascade/internal/notests	[no test files]
`

// TestParseBenchOutput_RealTranscript exercises the real entry point on a
// two-package, mixed no-test-files transcript shaped exactly like a real
// `go test ./... -bench=. -benchmem` run.
func TestParseBenchOutput_RealTranscript(t *testing.T) {
	results, err := ParseBenchOutput(strings.NewReader(sampleTranscript))
	if err != nil {
		t.Fatalf("ParseBenchOutput: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	want := []build.BudgetResult{
		{Name: "github.com/acamarata/cascade/providers/sqlite/BenchmarkSQLite-8", NsPerOp: 123.4, BytesPerOp: 45, AllocsPerOp: 2},
		{Name: "github.com/acamarata/cascade/internal/fleet/BenchmarkHeadroomPublish-8", NsPerOp: 246.8, BytesPerOp: 90, AllocsPerOp: 4},
	}
	for i, w := range want {
		if results[i] != w {
			t.Errorf("result[%d] = %+v, want %+v", i, results[i], w)
		}
	}
}

// TestParseBenchOutput_NoPackageHeader refuses a benchmark line with no
// preceding package-boundary line, fail-closed rather than qualifying it
// with an empty import path.
func TestParseBenchOutput_NoPackageHeader(t *testing.T) {
	orphan := "BenchmarkOrphan-8   	 1000000	       10.0 ns/op\n"
	_, err := ParseBenchOutput(strings.NewReader(orphan))
	if err == nil {
		t.Fatal("expected an error for a benchmark line with no package header, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("want a KindInvalidInput cascade.Error, got kind=%v ok=%v err=%v", kind, ok, err)
	}
}

// TestParseBenchOutput_NoBenchMem parses a line with no -benchmem fields,
// asserting the optional B/op and allocs/op groups default to zero
// rather than erroring.
func TestParseBenchOutput_NoBenchMem(t *testing.T) {
	transcript := "ok  	github.com/acamarata/cascade/internal/conductor	0.1s\n" +
		"BenchmarkQuota-8   	 2000000	       55.5 ns/op\n" +
		"PASS\nok  	github.com/acamarata/cascade/internal/conductor	0.2s\n"
	results, err := ParseBenchOutput(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("ParseBenchOutput: %v", err)
	}
	if len(results) != 1 || results[0].NsPerOp != 55.5 || results[0].BytesPerOp != 0 || results[0].AllocsPerOp != 0 {
		t.Fatalf("got %+v, want one result with NsPerOp=55.5 and zero mem fields", results)
	}
}

// TestGate_EmptyBudgetsIsClean asserts Gate returns nil against any
// result set when Budgets is empty, matching HEAD's shipped state.
func TestGate_EmptyBudgetsIsClean(t *testing.T) {
	if v := Gate([]build.BudgetResult{{Name: "x", NsPerOp: 1e9}}); v != nil {
		t.Fatalf("Gate on empty Budgets = %v, want nil", v)
	}
}
