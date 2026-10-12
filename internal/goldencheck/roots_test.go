package goldencheck

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// put writes one synthetic file below dir, creating parents.
func put(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// syntheticTree builds a tree holding every root kind plus decoys that must
// be ignored.
func syntheticTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{
		"internal/plain/testdata/v1-goldens/a.json",
		"internal/box/testdata/v1-goldens/migration/m.json",
		"internal/both/testdata/v1-goldens/b.json",
		"internal/both/testdata/v1-goldens/migration/m.json",
		"internal/mcp/testdata/goldens/claude-code/replay.json",
		".claude/testdata/v1-goldens/ignored.json",
		"node_modules/x/testdata/v1-goldens/ignored.json",
		"internal/other/testdata/goldens/not-claude/c.json",
	} {
		put(t, dir, rel)
	}
	return dir
}

func TestDiscoverRootsFindsAllKinds(t *testing.T) {
	dir := syntheticTree(t)
	got, err := DiscoverRoots(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"internal/both/testdata/v1-goldens",
		"internal/both/testdata/v1-goldens/migration",
		"internal/box/testdata/v1-goldens/migration",
		"internal/mcp/testdata/goldens/claude-code",
		"internal/plain/testdata/v1-goldens",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	owners := make([]Row, len(want))
	for i, root := range want {
		owners[i] = Row{Root: root, Package: "./x", RunRegex: "^TestX$"}
	}
	rows, err := Resolve(got, owners, nil)
	if err != nil || len(rows) != len(want) {
		t.Fatalf("resolve = %v, %v", rows, err)
	}
	if _, err := DiscoverRoots(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing module root must fail")
	}
}

func TestDiscoverRootsContainerOnlyIsNotARoot(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "p/testdata/v1-goldens/migration/m.json")
	got, err := DiscoverRoots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"p/testdata/v1-goldens/migration"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	put(t, dir, "p/testdata/v1-goldens/extra.json")
	got, _ = DiscoverRoots(dir)
	if len(got) != 2 {
		t.Fatalf("a v1-goldens dir with its own content is a root: %v", got)
	}
}

func TestResolveRejectsUnmappedRoot(t *testing.T) {
	roots := []string{"a/v1-goldens", "b/v1-goldens", "c/v1-goldens"}
	owners := []Row{{Root: "a/v1-goldens", Package: "./a", RunRegex: "^TestA$"}}
	excluded := map[string]string{"c/v1-goldens": "evidence only"}
	if _, err := Resolve(roots, owners, excluded); err == nil || !strings.Contains(err.Error(), "b/v1-goldens") {
		t.Fatalf("unmapped root must be named, got %v", err)
	}
	if strings.Contains(errString(Resolve(roots, owners, excluded)), "c/v1-goldens") {
		t.Fatal("an excluded root must not be reported")
	}
	rows, err := Resolve([]string{"a/v1-goldens", "c/v1-goldens"}, owners, excluded)
	if err != nil || len(rows) != 1 || rows[0].Root != "a/v1-goldens" {
		t.Fatalf("excluded root passes: %v, %v", rows, err)
	}
}

func errString(_ []Row, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestResolveRejectsBadTables(t *testing.T) {
	good := Row{Root: "a", Package: "./a", RunRegex: "^TestA$"}
	cases := []struct {
		name     string
		roots    []string
		owners   []Row
		excluded map[string]string
		want     string
	}{
		{"stale row", nil, []Row{good}, nil, "does not exist: a"},
		{"stale exclusion", nil, nil, map[string]string{"z": "why"}, "does not exist: z"},
		{"duplicate", []string{"a"}, []Row{good, good}, nil, "duplicate owner row"},
		{"empty field", []string{"a"}, []Row{{Root: "a"}}, nil, "empty field"},
		{"bad regex", []string{"a"}, []Row{{Root: "a", Package: "./a", RunRegex: "("}}, nil, "bad regex"},
		{"no reason", []string{"a"}, nil, map[string]string{"a": " "}, "has no reason"},
		{"both", []string{"a"}, []Row{good}, map[string]string{"a": "why"}, "both an owner row and an exclusion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.roots, tc.owners, tc.excluded)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// moduleRoot finds the module root from this source file's location.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func TestOwnerTableMatchesTree(t *testing.T) {
	root := moduleRoot(t)
	roots, err := DiscoverRoots(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Resolve(roots, Owners, Excluded)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(Owners) {
		t.Fatalf("resolved %d rows, table has %d", len(rows), len(Owners))
	}
}

// TestOwnerTableNamesRealTests proves every test an exact-name regex names is
// declared in its package, so a rename shows up here before the lane runs.
func TestOwnerTableNamesRealTests(t *testing.T) {
	root := moduleRoot(t)
	for _, row := range Owners {
		m := namedRegex.FindStringSubmatch(row.RunRegex)
		if m == nil {
			t.Errorf("%s: regex %q is not an exact-name list", row.Root, row.RunRegex)
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(row.Package, "./")))
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil || len(files) == 0 {
			t.Errorf("%s: package %s has no test files (%v)", row.Root, row.Package, err)
			continue
		}
		var src strings.Builder
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src.Write(data)
		}
		for _, name := range strings.Split(m[1], "|") {
			if !strings.Contains(src.String(), "func "+name+"(") {
				t.Errorf("%s: %s declares no %s", row.Root, row.Package, name)
			}
		}
	}
}
