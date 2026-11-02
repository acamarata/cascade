package repo

// SPORT: repo/symbol-dependency-graph/ADD scoped-load test coverage (P1-GEN-04).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const maintFixture = "testdata/fixture-maint"

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExtractPackagesMergesOnlyReloaded(t *testing.T) {
	ctx := context.Background()
	full, err := goExtractor{}.Extract(ctx, maintFixture)
	if err != nil {
		t.Fatal(err)
	}
	var loads [][]string
	onGraphLoad = func(p []string) { loads = append(loads, p) }
	t.Cleanup(func() { onGraphLoad = func([]string) {} })
	part, err := goExtractor{}.ExtractPackages(ctx, maintFixture, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loads) != 1 || len(loads[0]) != 1 || loads[0][0] != "./b" {
		t.Fatalf("loader patterns = %v, want [./b]", loads)
	}
	for _, n := range part.Nodes {
		if n.Package != "fixturemaint/b" {
			t.Fatalf("scoped load returned node %+v outside package b", n)
		}
	}
	// reloading b into the full graph reproduces the full graph byte for byte
	if got := MergePackages(*full, []string{"fixturemaint/b"}, *part); mustJSON(t, got) != mustJSON(t, *full) {
		t.Errorf("merge of an unchanged package altered the graph:\n%s\nwant\n%s", mustJSON(t, got), mustJSON(t, *full))
	}
	// an empty reload drops the package, its edges and edges into it
	dropped := MergePackages(*full, []string{"fixturemaint/a"}, SymbolGraph{})
	if err := dropped.Validate(); err != nil {
		t.Fatalf("graph after dropping a is invalid: %v", err)
	}
	for _, n := range dropped.Nodes {
		if n.Package == "fixturemaint/a" {
			t.Errorf("node %+v of the dropped package survived", n)
		}
	}
	for _, p := range []string{"fixturemaint/b", "fixturemaint/c"} {
		if packageNodesJSON(t, dropped, p) != packageNodesJSON(t, *full, p) && p == "fixturemaint/b" {
			t.Errorf("untouched package %s changed", p)
		}
	}
}

func TestExtractPackagesRefusesEscapingDir(t *testing.T) {
	_, err := goExtractor{}.ExtractPackages(context.Background(), maintFixture, []string{"../x"})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if _, err := (goExtractor{}).ExtractPackages(context.Background(), maintFixture, nil); err != nil {
		t.Fatalf("no dirs must be an empty graph, got %v", err)
	}
}

func TestMergePackagesDropsDeletedPackage(t *testing.T) {
	full, err := goExtractor{}.Extract(context.Background(), maintFixture)
	if err != nil {
		t.Fatal(err)
	}
	g := MergePackages(*full, []string{"fixturemaint/a"}, SymbolGraph{})
	for _, e := range g.Edges {
		if e.To == "fixturemaint/a" {
			t.Errorf("dangling edge %+v into the deleted package", e)
		}
	}
}

// A go.mod whose module line carries a trailing comment is valid Go; the
// graph leg must still read its module path.
func TestModulePathAcceptsTrailingComment(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "// header\nmodule \"example.com/m\" // the module\n\ngo 1.22\n", 0o644)
	got, err := modulePath(root)
	if err != nil || got != "example.com/m" {
		t.Fatalf("modulePath = %q, %v; want example.com/m", got, err)
	}
	mustWrite(t, filepath.Join(root, "go.mod"), "go 1.22\n", 0o644)
	var ce *cascade.Error
	if _, err := modulePath(root); !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
		t.Fatalf("no module line err = %v, want KindInvalidInput", err)
	}
}

// A go.mod that cannot be stat'ed for a reason other than absence is a leg
// failure, never a silently skipped graph leg (fail closed).
func TestLegGraphFailsWhenGoModUnreadable(t *testing.T) {
	m := &Maintenance{d: MaintenanceDeps{Root: "bad\x00root", RepositoryID: testRepoID}}
	err := m.legGraph(context.Background(), &pathRun{rel: "b/b.go"})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindUnavailable {
		t.Fatalf("legGraph err = %v, want KindUnavailable", err)
	}
	if err := (&Maintenance{d: MaintenanceDeps{Root: t.TempDir()}}).legGraph(context.Background(), &pathRun{rel: "b/b.go"}); err != nil {
		t.Fatalf("no go.mod must skip the graph leg, got %v", err)
	}
}
