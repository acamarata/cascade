// Package build (this file) proves atomicwritegate.go: GREEN on the real
// tree with its owned exemption list, RED on every seeded violation under
// testdata/seeded-violations/atomicwrite/, and GREEN on the seeded clean
// control, so a RED case is red for its defect and not for its fixture.
package build

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// atomicWriteSeededDir is the seeded-violation fixture root.
func atomicWriteSeededDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(outputgateModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "atomicwrite")
}

// atomicWriteSeededConfig is the gate config for one seeded case: a low
// file floor and the fixture's own main.go as the required file.
func atomicWriteSeededConfig(t *testing.T, name string) atomicWriteConfig {
	t.Helper()
	dir := filepath.Join(atomicWriteSeededDir(t), name)
	return atomicWriteConfig{Root: dir, TSV: filepath.Join(dir, "exemptions.tsv"), MinFiles: 2, Required: []string{"cmd/app/main.go"}}
}

func TestNoBareFileWrites_Live(t *testing.T) {
	root := outputgateModuleRoot(t)
	cfg := atomicWriteConfig{
		Root:     root,
		TSV:      filepath.Join(root, "internal", "build", "testdata", "atomicwrite-exemptions.tsv"),
		MinFiles: 500,
		Required: []string{"cmd/cascade/main.go", "internal/runtime/atomic_write.go"},
	}
	if problems := checkAtomicWrites(cfg); len(problems) != 0 {
		t.Fatalf("atomic-write gate: %d problem(s) in the real tree:\n%s", len(problems), strings.Join(problems, "\n"))
	}
	scan, err := scanAtomicWriteTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, rowProblems := loadAtomicWriteRows(cfg.TSV)
	if len(rowProblems) != 0 {
		t.Fatalf("exemption list: %v", rowProblems)
	}
	// Guard the absence check against an empty walk: the gate saw real
	// writes, and every one of them sits under a row.
	if scan.files < 500 || len(scan.hits) == 0 {
		t.Fatalf("live scan parsed %d files with %d WriteFile references; the walk proves nothing", scan.files, len(scan.hits))
	}
	if problems := checkAtomicWriteOwnedRows(rows); len(problems) != 0 {
		t.Fatalf("exemption list is not exactly the owned rows:\n%s", strings.Join(problems, "\n"))
	}
	t.Logf("live scan: %d files, %d owned WriteFile references, %d rows", scan.files, len(scan.hits), len(rows))
}

// atomicWriteOwnedRows is the only exemption set the gate may carry: the
// key write its owning ticket still has to move to
// runtime.CreateFileAtomic. Another row needs this list edited with it.
var atomicWriteOwnedRows = map[[3]string]bool{
	{"internal/elevation/keystore_file.go", "fileKeystore.GenerateKey", "P1-SEC-00"}: true,
}

// checkAtomicWriteOwnedRows reports every row outside the owned set and
// every owned row that is missing, so the live list must equal the set.
func checkAtomicWriteOwnedRows(rows []atomicWriteRow) []string {
	var problems []string
	seen := map[[3]string]bool{}
	for _, r := range rows {
		key := [3]string{r.File, r.Symbol, r.Owner}
		if !atomicWriteOwnedRows[key] {
			problems = append(problems, "unexpected exemption row "+r.File+" "+r.Symbol+" owner "+r.Owner)
			continue
		}
		seen[key] = true
	}
	for key := range atomicWriteOwnedRows {
		if !seen[key] {
			problems = append(problems, "owned row missing: "+key[0]+" "+key[1]+" owner "+key[2])
		}
	}
	sort.Strings(problems)
	return problems
}

// TestAtomicWriteOwnedRows_RejectsExtraRow proves the exact-set check can
// fail: the owned row passes, an extra row fails, and a dropped owned row
// fails.
func TestAtomicWriteOwnedRows_RejectsExtraRow(t *testing.T) {
	owned := []atomicWriteRow{
		{File: "internal/elevation/keystore_file.go", Symbol: "fileKeystore.GenerateKey", Owner: "P1-SEC-00", Count: 1},
	}
	if problems := checkAtomicWriteOwnedRows(owned); len(problems) != 0 {
		t.Fatalf("the owned row must pass, got %v", problems)
	}
	extra := append(append([]atomicWriteRow{}, owned...), atomicWriteRow{File: "internal/a/a.go", Symbol: "Save", Owner: "P1-CORE-99", Count: 1})
	if problems := checkAtomicWriteOwnedRows(extra); len(problems) != 1 || !strings.Contains(problems[0], "unexpected exemption row internal/a/a.go Save") {
		t.Fatalf("an extra row must fail on that row alone, got %v", problems)
	}
	if problems := checkAtomicWriteOwnedRows(owned[:0]); len(problems) != 1 || !strings.Contains(problems[0], "owned row missing: internal/elevation/keystore_file.go") {
		t.Fatalf("a dropped owned row must fail, got %v", problems)
	}
	// Same file and symbol under a different owner is a different row.
	swapped := []atomicWriteRow{{File: owned[0].File, Symbol: owned[0].Symbol, Owner: "P1-CORE-98", Count: 1}}
	if problems := checkAtomicWriteOwnedRows(swapped); len(problems) != 2 {
		t.Fatalf("a re-owned row must fail as one extra and one missing, got %v", problems)
	}
}

func TestNoBareFileWrites_Seeded(t *testing.T) {
	cases := []struct {
		name string
		want string
		cfg  func(c *atomicWriteConfig)
	}{
		{"unlisted_os", "unlisted bare write internal/a/a.go:7 in Save: os.WriteFile", nil},
		{"unlisted_ioutil", "unlisted bare write internal/a/a.go:7 in Save: ioutil.WriteFile", nil},
		{"aliased_os", "unlisted bare write internal/a/a.go:7 in Save: os.WriteFile", nil},
		{"method_value", "unlisted bare write internal/a/a.go:7 in Save: os.WriteFile", nil},
		{"dot_import", "dot-import:os cannot be gated", nil},
		{"stale_row", "stale row internal/a/a.go Save", nil},
		{"unowned_row", `owner "UNOWNED" is not a PEWTT ticket id`, nil},
		{"empty_reason", "empty reason", nil},
		{"missing_tsv", "exemption list missing or unreadable", nil},
		{"malformed_row", "want 5 tab-separated columns, got 4", nil},
		{"duplicate_row", "duplicate row internal/a/a.go Save", nil},
		{"wrong_header", "exemption list header is", nil},
		{"count_mismatch", "count mismatch for internal/a/a.go Save: row says 1, found 2", nil},
		{"too_few_files", "fewer than 500: the walk is broken", func(c *atomicWriteConfig) { c.MinFiles = 500 }},
		{"missing_required", "scan never visited cmd/cascade/main.go", func(c *atomicWriteConfig) {
			c.Required = append(c.Required, "cmd/cascade/main.go")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := atomicWriteSeededConfig(t, tc.name)
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			problems := checkAtomicWrites(cfg)
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Fatalf("seeded %s: gate did not fail with %q; problems:\n%s", tc.name, tc.want, strings.Join(problems, "\n"))
		})
	}
}

// TestNoBareFileWrites_SeededCleanControl: the clean fixture (an owned
// write, a non-gated os.ReadFile) passes, so each RED case above is red for
// its seeded defect alone.
func TestNoBareFileWrites_SeededCleanControl(t *testing.T) {
	if problems := checkAtomicWrites(atomicWriteSeededConfig(t, "clean")); len(problems) != 0 {
		t.Fatalf("clean control failed:\n%s", strings.Join(problems, "\n"))
	}
}

func TestAtomicWriteSymbol_Receivers(t *testing.T) {
	src := "package a\n\nimport \"os\"\n\nvar w = os.WriteFile\n\n" +
		"type Store[K comparable] struct{}\n\n" +
		"func (s *Store[K]) Put(p string) error { return os.WriteFile(p, nil, 0o600) }\n\n" +
		"func (s Store[K]) Get(p string) error { return os.WriteFile(p, nil, 0o600) }\n"
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	hits, err := scanAtomicWriteFile(path, "internal/a/a.go")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.Symbol)
	}
	if strings.Join(got, ",") != "(package),Store.Put,Store.Get" {
		t.Fatalf("symbols = %v, want [(package) Store.Put Store.Get]", got)
	}
}
