// Package build holds build-time gates on the repo tree itself. This file
// implements the import-boundary and no-cycle arch gates from
// P1-E01-W1-S01-T2 (12-QUALITY-CONSTITUTION.md Art.10.2): plugins/**+
// providers/** import pkg/** only (never internal/**); pkg/** never
// imports internal/**; cmd/** is the sole composition root; the module's
// internal import graph has no cycles. golangci-lint's depguard carries the
// first two at lint time (.golangci.yml); this file is the independent
// Go-level layer the contract also requires. Each rule is RED against a
// fixture materialized into t.TempDir() (Art.7.1) and GREEN on the real
// tree; fixtures live under testdata/ so the toolchain never compiles them
// (Art.1).
package build

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// cascadeModulePath is this repo's module path (go.mod). Boundary fixtures
// reuse it; the cycle fixture uses its own fake prefix (self-consistency
// only).
const cascadeModulePath = "github.com/acamarata/cascade"

// archFile is one non-test .go file: its dir relative to the scanned root
// ("" for root level) and its module-internal imports.
type archFile struct {
	relDir  string
	imports []string
}

// archModuleRoot locates the repo root by walking up from this file.
func archModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("arch gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("arch gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// archIsSkippedDir excludes testdata/ (fixtures) and dot dirs (.git, ...).
func archIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// archScan walks root for non-test .go files and returns each one's
// directory plus the modulePrefix-rooted imports it declares.
func archScan(t *testing.T, root, modulePrefix string) []archFile {
	t.Helper()
	var out []archFile
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if archIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("arch gate: parsing %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			t.Fatalf("arch gate: relativizing %s: %v", path, err)
		}
		relDir := filepath.ToSlash(rel)
		if relDir == "." {
			relDir = ""
		}
		var imports []string
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == modulePrefix || strings.HasPrefix(p, modulePrefix+"/") {
				imports = append(imports, p)
			}
		}
		out = append(out, archFile{relDir: relDir, imports: imports})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("arch gate: walking %s: %v", root, walkErr)
	}
	return out
}

// archMaterialize copies the fixture tree at src into t.TempDir() (Art.7.1)
// and returns the copy's root.
func archMaterialize(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if walkErr != nil {
		t.Fatalf("arch gate: materializing fixture %s: %v", src, walkErr)
	}
	return dst
}

// archDirHasPrefix: "plugins" matches "plugins"/"plugins/foo", never "pluginsx".
func archDirHasPrefix(relDir, prefix string) bool {
	return relDir == prefix || strings.HasPrefix(relDir, prefix+"/")
}

// archViolations returns, sorted, "<dir> imports <path>" for matched files
// whose import reaches modulePrefix's internal/**.
func archViolations(files []archFile, match func(relDir string) bool, modulePrefix string) []string {
	var out []string
	internalPrefix := modulePrefix + "/internal"
	for _, f := range files {
		if !match(f.relDir) {
			continue
		}
		for _, imp := range f.imports {
			if imp == internalPrefix || strings.HasPrefix(imp, internalPrefix+"/") {
				out = append(out, f.relDir+" imports "+imp)
			}
		}
	}
	sort.Strings(out)
	return out
}

// archBoundaryRule is one import-boundary rule: its RED fixture and files.
type archBoundaryRule struct {
	name    string
	fixture string
	match   func(relDir string) bool
}

var archBoundaryRules = []archBoundaryRule{
	{
		name:    "plugins-providers-import-pkg-only",
		fixture: "plugins-providers-boundary",
		match: func(d string) bool {
			return archDirHasPrefix(d, "plugins") || archDirHasPrefix(d, "providers")
		},
	},
	{
		name:    "pkg-no-internal",
		fixture: "pkg-no-internal",
		match:   func(d string) bool { return archDirHasPrefix(d, "pkg") },
	},
	{
		name:    "composition-root",
		fixture: "composition-root",
		match: func(d string) bool {
			return !archDirHasPrefix(d, "cmd") && !archDirHasPrefix(d, "internal")
		},
	},
}

// TestArchImportBoundaries_RealTreeGreen: no boundary rule fires on the real tree.
func TestArchImportBoundaries_RealTreeGreen(t *testing.T) {
	root := archModuleRoot(t)
	files := archScan(t, root, cascadeModulePath)
	for _, rule := range archBoundaryRules {
		t.Run(rule.name, func(t *testing.T) {
			v := archViolations(files, rule.match, cascadeModulePath)
			if len(v) != 0 {
				t.Fatalf("%s violated on real tree:\n%s", rule.name, strings.Join(v, "\n"))
			}
		})
	}
}

// TestArchImportBoundaries_SeededViolationRed: each rule's fixture trips it.
func TestArchImportBoundaries_SeededViolationRed(t *testing.T) {
	root := archModuleRoot(t)
	for _, rule := range archBoundaryRules {
		t.Run(rule.name, func(t *testing.T) {
			src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", rule.fixture)
			fixtureRoot := archMaterialize(t, src)
			files := archScan(t, fixtureRoot, cascadeModulePath)
			v := archViolations(files, rule.match, cascadeModulePath)
			if len(v) == 0 {
				t.Fatalf("%s: expected a violation in seeded fixture %s, found none", rule.name, src)
			}
		})
	}
}

// archBuildGraph: import-path -> imported import-path, modulePrefix edges only.
func archBuildGraph(files []archFile, modulePrefix string) map[string][]string {
	g := map[string][]string{}
	for _, f := range files {
		pkg := modulePrefix
		if f.relDir != "" {
			pkg = modulePrefix + "/" + f.relDir
		}
		g[pkg] = append(g[pkg], f.imports...)
	}
	return g
}

// archFindCycle runs a deterministic DFS and returns the first cycle found
// (ordered path back to its start), or nil.
func archFindCycle(g map[string][]string) []string {
	const white, gray, black = 0, 1, 2
	color := map[string]int{}
	var path, cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = gray
		path = append(path, n)
		for _, m := range g[n] {
			if color[m] == gray {
				idx := 0
				for i, p := range path {
					if p == m {
						idx = i
						break
					}
				}
				cycle = append(append([]string{}, path[idx:]...), m)
				return true
			}
			if color[m] == white && visit(m) {
				return true
			}
		}
		path = path[:len(path)-1]
		color[n] = black
		return false
	}
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if color[k] == white && visit(k) {
			return cycle
		}
	}
	return nil
}

// TestArchNoImportCycles_RealTreeGreen: the real internal import graph is acyclic.
func TestArchNoImportCycles_RealTreeGreen(t *testing.T) {
	root := archModuleRoot(t)
	files := archScan(t, root, cascadeModulePath)
	g := archBuildGraph(files, cascadeModulePath)
	if cycle := archFindCycle(g); cycle != nil {
		t.Fatalf("import cycle found in real tree: %s", strings.Join(cycle, " -> "))
	}
}

// TestArchNoImportCycles_SeededViolationRed: the fixture's two packages
// import each other.
func TestArchNoImportCycles_SeededViolationRed(t *testing.T) {
	root := archModuleRoot(t)
	src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "import-cycle")
	fixtureRoot := archMaterialize(t, src)
	const fixtureModule = "example.com/archfixture"
	files := archScan(t, fixtureRoot, fixtureModule)
	g := archBuildGraph(files, fixtureModule)
	if cycle := archFindCycle(g); cycle == nil {
		t.Fatal("expected an import cycle in seeded fixture, found none")
	}
}
