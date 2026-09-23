package ci

import (
	"context"
	"testing"
)

// fixtureRoot is internal/ci/testdata/affected_go_fixture: a real,
// independently-buildable Go module (own go.mod) where pkg/b imports
// pkg/a -- see its own README.md for provenance.
const fixtureRoot = "testdata/affected_go_fixture"

// TestAffectedTargets_GoFixtureCrossCuttingChange proves a change to
// pkg/a (the fixture's leaf package) marks BOTH pkg/a and its importer
// pkg/b affected -- the real go list subprocess, no test double (Art.2).
func TestAffectedTargets_GoFixtureCrossCuttingChange(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"pkg/a/a.go"})
	if err != nil {
		t.Fatalf("affectedGoTargets: %v", err)
	}
	want := []Target{"example.com/fixture/pkg/a", "example.com/fixture/pkg/b"}
	assertTargetsEqual(t, targets, want)
}

// TestAffectedTargets_GoFixtureIsolatedChange proves a change to pkg/b
// (which nothing imports) marks pkg/b alone.
func TestAffectedTargets_GoFixtureIsolatedChange(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"pkg/b/b.go"})
	if err != nil {
		t.Fatalf("affectedGoTargets: %v", err)
	}
	want := []Target{"example.com/fixture/pkg/b"}
	assertTargetsEqual(t, targets, want)
}

// assertTargetAllFallback proves targets is exactly the []Target{TargetAll}
// fail-closed fallback shape (D1 REWORK fix), never an error and never
// an empty or partial set.
func assertTargetAllFallback(t *testing.T, targets []Target, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("affectedGoTargets: %v", err)
	}
	if len(targets) != 1 || targets[0] != TargetAll {
		t.Fatalf("affectedGoTargets = %v, want [%s] (TargetAll)", targets, TargetAll)
	}
}

// TestAffectedTargets_GoModChangeForcesFull proves a go.mod-only change
// forces TargetAll rather than being silently dropped (D1 REWORK fix;
// inverts the former TestAffectedTargets_GoFixtureNonPackageChangeIgnored,
// which wrongly asserted an empty result for exactly this input).
func TestAffectedTargets_GoModChangeForcesFull(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"go.mod"})
	assertTargetAllFallback(t, targets, err)
}

// TestAffectedTargets_GoSumChangeForcesFull proves a go.sum-only change
// forces TargetAll, matching go.mod's treatment (D1).
func TestAffectedTargets_GoSumChangeForcesFull(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"go.sum"})
	assertTargetAllFallback(t, targets, err)
}

// TestAffectedTargets_DeletedPackageDirForcesFull proves a changed path
// under a directory that no longer holds a buildable package (simulating
// a deleted package -- the directory never resolves via go list) forces
// TargetAll rather than contributing nothing (D1).
func TestAffectedTargets_DeletedPackageDirForcesFull(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"pkg/deleted/old.go"})
	assertTargetAllFallback(t, targets, err)
}

// TestAffectedTargets_RootLevelNonPackageFileForcesFull proves a changed
// path at the module root that is not itself in a package (the fixture
// root holds no .go files) forces TargetAll (D1) -- distinct from the
// go.mod/go.sum literal-filename rule: README.md is caught by the
// general directory-resolution failure, not the filename check.
func TestAffectedTargets_RootLevelNonPackageFileForcesFull(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"README.md"})
	assertTargetAllFallback(t, targets, err)
}

// TestAffectedTargets_TestdataFileMapsToEnclosingPackage proves a changed
// path nested under a package's testdata/ directory maps to that
// package (walking up to the first directory go list resolves), not to
// TargetAll and not dropped (D1). pkg/a/testdata/sample.txt should
// behave exactly like a pkg/a/a.go change: both pkg/a and its importer
// pkg/b are affected.
func TestAffectedTargets_TestdataFileMapsToEnclosingPackage(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"pkg/a/testdata/sample.txt"})
	if err != nil {
		t.Fatalf("affectedGoTargets: %v", err)
	}
	want := []Target{"example.com/fixture/pkg/a", "example.com/fixture/pkg/b"}
	assertTargetsEqual(t, targets, want)
}

// TestAffectedTargets_NonGoFileInPackageDirMapsToPackage proves a
// changed non-Go file living directly inside a package's own directory
// (an embed source / .s-file stand-in) maps to that package (D1).
// pkg/b/asset.txt should behave like a pkg/b/b.go change: pkg/b alone
// (nothing imports pkg/b in production code).
func TestAffectedTargets_NonGoFileInPackageDirMapsToPackage(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"pkg/b/asset.txt"})
	if err != nil {
		t.Fatalf("affectedGoTargets: %v", err)
	}
	want := []Target{"example.com/fixture/pkg/b"}
	assertTargetsEqual(t, targets, want)
}

// TestAffectedTargets_TestOnlyImporterSelected proves the reverse-import
// walk follows _test.go import edges (D3): pkg/c is imported only by
// pkg/b's in-package test file (b_test.go), never by any production
// source, yet a pkg/c change must still mark pkg/b affected alongside
// pkg/c itself.
func TestAffectedTargets_TestOnlyImporterSelected(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), fixtureRoot, []string{"pkg/c/c.go"})
	if err != nil {
		t.Fatalf("affectedGoTargets: %v", err)
	}
	want := []Target{"example.com/fixture/pkg/b", "example.com/fixture/pkg/c"}
	assertTargetsEqual(t, targets, want)
}

// TestAffectedTargets_GoWorktreeRootMissing proves an absent worktree
// root is a typed error, not a panic.
func TestAffectedTargets_GoWorktreeRootMissing(t *testing.T) {
	_, err := affectedGoTargets(context.Background(), "testdata/does-not-exist-xyz", []string{"pkg/a/a.go"})
	if err == nil {
		t.Fatal("affectedGoTargets(missing worktree root) returned nil error")
	}
}

// TestAffectedTargets_UnrelatedBrokenPackageForcesFull proves D1's
// remaining gap from the confirm round 2 review (case 3): a healthy,
// UNTOUCHED, UNRELATED package elsewhere in the tree that fails to `go
// list` (testdata/affected_go_fixture_broken/pkg/broken, a package
// directory holding a real .go file plus an orphan cgo-less .c file)
// must not turn the whole-tree import-graph subprocess's failure into a
// raw, unhandled error a caller could read as "nothing affected" or
// abort on. affectedGoTargets must fail CLOSED to TargetAll instead,
// exactly like every other "cannot compute" branch in this file --
// changed here names ONLY the healthy, unrelated pkg/healthy/healthy.go.
func TestAffectedTargets_UnrelatedBrokenPackageForcesFull(t *testing.T) {
	targets, err := affectedGoTargets(context.Background(), "testdata/affected_go_fixture_broken", []string{"pkg/healthy/healthy.go"})
	assertTargetAllFallback(t, targets, err)
}

func assertTargetsEqual(t *testing.T, got, want []Target) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}
}

// TestParseImportGraph_ValidTwoPackage proves the real fixture's own
// output shape parses into the expected direct-edge map.
func TestParseImportGraph_ValidTwoPackage(t *testing.T) {
	data := []byte("example.com/fixture/pkg/a|\nexample.com/fixture/pkg/b|example.com/fixture/pkg/a,fmt\n")
	graph, err := parseImportGraph(data)
	if err != nil {
		t.Fatalf("parseImportGraph: %v", err)
	}
	if len(graph["example.com/fixture/pkg/a"]) != 0 {
		t.Fatalf("pkg/a imports = %v, want none", graph["example.com/fixture/pkg/a"])
	}
	want := []string{"example.com/fixture/pkg/a", "fmt"}
	got := graph["example.com/fixture/pkg/b"]
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pkg/b imports = %v, want %v", got, want)
	}
}

// TestParseImportGraph_BlankLinesSkipped proves blank lines (including a
// trailing newline) are tolerated, not treated as malformed.
func TestParseImportGraph_BlankLinesSkipped(t *testing.T) {
	graph, err := parseImportGraph([]byte("\n\nexample.com/fixture/pkg/a|\n\n"))
	if err != nil {
		t.Fatalf("parseImportGraph: %v", err)
	}
	if len(graph) != 1 {
		t.Fatalf("graph = %v, want exactly one entry", graph)
	}
}

// TestParseImportGraph_MalformedLineReturnsError proves a line without
// the "|" separator is a typed error, not a silently dropped row.
func TestParseImportGraph_MalformedLineReturnsError(t *testing.T) {
	if _, err := parseImportGraph([]byte("this is not go list output")); err == nil {
		t.Fatal("parseImportGraph(malformed) returned nil error")
	}
}

// TestParseImportGraph_EmptyImportPathReturnsError proves a line whose
// import path is blank (a "|" with nothing before it) is refused.
func TestParseImportGraph_EmptyImportPathReturnsError(t *testing.T) {
	if _, err := parseImportGraph([]byte("|fmt,os")); err == nil {
		t.Fatal("parseImportGraph(empty import path) returned nil error")
	}
}

// TestReachableLocal_IncludesStart proves a package always reaches
// itself, even with no outgoing edges.
func TestReachableLocal_IncludesStart(t *testing.T) {
	graph := map[string][]string{"a": nil}
	local := map[string]bool{"a": true}
	reachable := reachableLocal(graph, "a", local)
	if !reachable["a"] {
		t.Fatal("reachableLocal did not include start package")
	}
	if len(reachable) != 1 {
		t.Fatalf("reachableLocal(a) = %v, want exactly {a}", reachable)
	}
}

// TestReachableLocal_PrunesExternalImports proves a std/external import
// is a dead end, never followed as if it were a local Target.
func TestReachableLocal_PrunesExternalImports(t *testing.T) {
	graph := map[string][]string{"a": {"fmt", "b"}, "b": nil}
	local := map[string]bool{"a": true, "b": true}
	reachable := reachableLocal(graph, "a", local)
	if reachable["fmt"] {
		t.Fatal("reachableLocal followed a non-local import")
	}
	if !reachable["b"] {
		t.Fatal("reachableLocal did not follow a local import")
	}
}
