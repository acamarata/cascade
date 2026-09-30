// Purpose: the live-anchor rule. A committed inventory row may carry
// decommission-safe "yes" only when its anchor symbol is not referenced by
// non-migration production code: a symbol v2 still uses at runtime is not
// something a later ticket may retire. Live v2 markers are therefore
// PROVENANCE ("no"), never DONE ("yes").
// Inputs: the real tracked tree (via realTreeRows) and a t.TempDir fixture
// that proves the detector is not vacuous.
// Constraints: default lane, no build tags; reads the module tree, writes
// only under t.TempDir().
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// liveMarkerRows are the (term, path) rows whose hits name the live v2
// managed-block marker (markerOpenPrefix in internal/context/harness_gen.go).
// Each must be PROVENANCE with decommission-safe "no". The two term names
// are split so this file never holds a term hit of its own.
const (
	termMarkerKebab = "generate" + "-instructions"
	termMarkerSnake = "generate" + "_instructions"
)

var liveMarkerRows = [][2]string{
	{termMarkerKebab, "docs/harness-conventions.md"},
	{termMarkerKebab, "internal/context/harness_gen.go"},
	{termMarkerKebab, "internal/migration/instruction_regen_apply_test.go"},
	{termMarkerKebab, "internal/migration/instruction_regen_test.go"},
	{termMarkerKebab, "plugins/opencode/opencode_test.go"},
	{termMarkerSnake, "internal/context/gen_cc.go"},
	{termMarkerSnake, "internal/context/gen_cc_sections.go"},
	{termMarkerSnake, "internal/context/harness_gen.go"},
}

// isProductionGo reports whether rel is a non-migration production Go
// file: a .go file that is not a test, not under testdata and not under
// internal/migration/.
func isProductionGo(rel string) bool {
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	if strings.HasPrefix(rel, "internal/migration/") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "testdata" {
			return false
		}
	}
	return true
}

// liveAnchorRefs returns every file in files (slash paths relative to
// root) that is non-migration production Go code and contains anchor.
func liveAnchorRefs(t *testing.T, root string, files []string, anchor string) []string {
	t.Helper()
	var refs []string
	for _, rel := range files {
		if !isProductionGo(rel) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // tracked path under the module root or t.TempDir
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		if bytes.Contains(data, []byte(anchor)) {
			refs = append(refs, rel)
		}
	}
	return refs
}

func TestResolve_LiveAnchorNeverDecommissionSafe(t *testing.T) {
	t.Run("detector", testLiveAnchorDetector)
	root, files, rows := realTreeRows(t)
	if len(rows) == 0 {
		t.Fatal("real-tree sweep produced no rows; the live-anchor rule would pass vacuously")
	}
	if refs := liveAnchorRefs(t, root, files, "markerOpenPrefix"); len(refs) == 0 {
		t.Fatal("markerOpenPrefix is not referenced by any non-migration production file; the detector is blind")
	}
	assertNoLiveYesRows(t, root, files, rows)
	assertLiveMarkerRowsProvenance(t, rows)
}

// testLiveAnchorDetector proves liveAnchorRefs is not vacuous: of six
// fixture files it picks exactly the non-migration production Go file
// holding the anchor (not a test, testdata, migration, doc or anchorless
// file).
func testLiveAnchorDetector(t *testing.T) {
	root := t.TempDir()
	const anchor = "zzFixtureLiveAnchor"
	files := []string{
		"pkg/live/live.go", "pkg/live/live_test.go", "pkg/live/testdata/x.go",
		"internal/migration/v1/m.go", "docs/live.md", "pkg/live/other.go",
	}
	for _, rel := range files {
		body := "package live // " + anchor + "\n"
		if rel == "pkg/live/other.go" {
			body = "package live\n"
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := liveAnchorRefs(t, root, files, anchor)
	if want := []string{"pkg/live/live.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("liveAnchorRefs = %v, want %v", got, want)
	}
}

// assertNoLiveYesRows fails for every decommission-safe "yes" row that has
// no anchor or whose anchor non-migration production code references.
func assertNoLiveYesRows(t *testing.T, root string, files []string, rows []Row) {
	t.Helper()
	cache := map[string][]string{}
	checked := 0
	for _, row := range rows {
		if row.decommissionSafe() != "yes" {
			continue
		}
		checked++
		anchor := row.Term.Anchor
		if strings.TrimSpace(anchor) == "" {
			t.Errorf("row (%s, %s) is decommission-safe \"yes\" with no anchor", row.Term.Term, row.Path)
			continue
		}
		refs, ok := cache[anchor]
		if !ok {
			refs = liveAnchorRefs(t, root, files, anchor)
			cache[anchor] = refs
		}
		if len(refs) > 0 {
			t.Errorf("row (%s, %s) is decommission-safe \"yes\" but its anchor %q is referenced by non-migration production code %v; declare the hit PROVENANCE (live v2 marker)",
				row.Term.Term, row.Path, anchor, refs)
		}
	}
	t.Logf("checked %d decommission-safe \"yes\" rows against non-migration production code", checked)
}

// assertLiveMarkerRowsProvenance fails unless every liveMarkerRows pair is
// present in rows as PROVENANCE with decommission-safe "no".
func assertLiveMarkerRowsProvenance(t *testing.T, rows []Row) {
	t.Helper()
	byKey := make(map[[2]string]Row, len(rows))
	for _, row := range rows {
		byKey[[2]string{row.Term.Term, row.Path}] = row
	}
	for _, k := range liveMarkerRows {
		row, ok := byKey[k]
		if !ok {
			t.Errorf("live marker row (%s, %s) is missing from the real-tree sweep", k[0], k[1])
			continue
		}
		if row.Disposition != DispositionProvenance || row.decommissionSafe() != "no" {
			t.Errorf("live marker row (%s, %s) is %s with decommission-safe %q, want PROVENANCE with \"no\"",
				k[0], k[1], row.Disposition, row.decommissionSafe())
		}
	}
}
