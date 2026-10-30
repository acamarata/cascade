package build

// Purpose: UnmodeledGuardedKeys fails every unmodeled read of a guarded config
//   section (guardedFamilies) in non-test code of cmd/, internal/, plugins/.
// Inputs: a module root. Outputs: sorted "file:line: key" findings; an error
//   when a package fails to load or type-check (linux, darwin, windows).
// Constraints: a read is a constant (or constant-valued local) index on a
//   map[string]interface{} through indexes, assertions, locals and helper calls
//   (a map then constant names), or a range over runtime.Config.Extra. It is
//   modeled when reachable from its family's registered Extract or a built-in,
//   or when the value is only handed to such a module function, read further,
//   nil-checked or kept in a local; else it needs a reasoned exemption row.
//   The AST walk lives in guardedconfiggate_walk.go.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

type guardedExemption struct{ Path, Key, Reason string }

const guardedRT = "github.com/acamarata/cascade/internal/runtime"

var (
	guardedConfigExemptions = []guardedExemption{
		{"internal/runtime/hotreload.go", "*", "freezeColdSections copies the whole tree to pin the cold sections; it reads no guarded key"},
		{"internal/runtime/config.go", "*", "EffectiveEntries flattens the whole tree for the read-only effective view (config show); it displays values and decides nothing"},
		{"internal/runtime/config_retrieval.go", "retrieval", "decodes sources/fusion/reranker; its closed key set refuses [retrieval.embedding]"},
		{"internal/runtime/config_retrieval_fusion.go", "retrieval", "reads [retrieval.fusion] only; a nested embedding read is its own key"}}
	// guardedConfigBuiltins read, decode or write back the built-in EffectiveConfig fields;
	// hooksFamilyApply is the [hooks] family's Apply, ApplyEffective's write-back of the
	// admitted list (its Extract, hooksFamilyExtract, is the registered reader).
	guardedConfigBuiltins = []string{guardedRT + ".extractEffectiveConfig", guardedRT + ".parseElevationSection",
		guardedRT + ".ApplyEffective", guardedRT + ".ApplySecretsThresholds", "(*" + guardedRT + ".HotReloader).applySecrets",
		"github.com/acamarata/cascade/cmd/cascade.hooksFamilyApply"}
	// guardedFamilies are the guarded sections; "*" is a range over the tree; a
	// parent ([agents], [retrieval]) counts only as the whole table.
	guardedFamilies = []string{"policy", "secrets", "sync", "nodes", "conductor", "elevation",
		"agents.egress", "agents.auth", "agents.lanes", "retrieval.embedding", "hooks", "*", "agents", "retrieval"}
)

type guardedSite struct {
	pos, fn, family, key string
	handoff              []string // the functions the value is handed to, if any
	arg                  int      // the argument position of a handoff
	safe                 bool     // a handoff, a nested read, a nil check or a local
	param                string   // "fn#i": a whole use of fn's tree parameter i
	computed, helper     bool     // the tree indexed by a computed key; handed to a section helper
}

type guardedGate struct {
	calls   map[string][]string // function -> functions it references
	roots   map[string][]string // family -> registered Extract functions
	sites   map[string]guardedSite
	info    *types.Info
	fset    *token.FileSet
	rel, fn string
	parent  map[ast.Node]ast.Node
	defs    map[types.Object][]ast.Expr // a local's assigned values (nil: unknown)
	params  map[types.Object]string     // the walked function's tree parameters ("fn#i")
	param   string                      // set while recording a tree parameter's use
	treeArg map[string]bool             // every "fn#i" that is a tree parameter
}

// UnmodeledGuardedKeys reports every unmodeled guarded read under root.
func UnmodeledGuardedKeys(root string) ([]string, error) {
	g := &guardedGate{calls: map[string][]string{}, roots: map[string][]string{}, sites: map[string]guardedSite{}, treeArg: map[string]bool{}}
	if err := g.load(root); err != nil {
		return nil, err
	}
	var out []string
	used, builtinReach := map[guardedExemption]bool{}, g.reach(guardedConfigBuiltins, map[string]bool{})
	for _, site := range g.sites {
		if site.param != "" {
			continue // judged only through a whole-tree handoff (wholeSafe)
		}
		roots := g.roots[site.family]
		for f, r := range g.roots { // a parent table ([agents]) is modeled by its sub-table families
			if strings.HasPrefix(f, site.family+".") {
				roots = append(roots, r...)
			}
		}
		reach := g.reach(roots, map[string]bool{})
		ours := func(f string) bool { _, walked := g.calls[f]; return walked && reach[f] } // never a stdlib function
		ok := reach[site.fn] || (builtinReach[site.fn] && !strings.Contains(site.family, ".") && !guardedParents[site.family]) ||
			site.family != "*" && site.safe && (site.handoff == nil || slices.ContainsFunc(site.handoff, ours)) ||
			site.family == "*" && g.wholeSafe(site, map[string]bool{})
		for _, e := range guardedConfigExemptions {
			if strings.HasPrefix(site.pos, e.Path+":") && e.Key == site.key && e.Reason != "" {
				ok, used[e] = true, true
			}
		}
		if !ok {
			out = append(out, site.pos+": "+site.key)
		}
	}
	for _, e := range guardedConfigExemptions {
		if !used[e] {
			out = append(out, e.Path+": exemption "+e.Key+" matches no unmodeled read (stale row, or no reason)")
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// wholeSafe: a whole-tree use is safe when it is read further, nil-checked or
// aliased, or handed (as the immediate argument) to a walked function whose
// tree parameter at that position is itself only used safely.
func (g *guardedGate) wholeSafe(s guardedSite, seen map[string]bool) bool {
	if !s.safe || s.handoff == nil {
		return s.safe
	}
	k := fmt.Sprintf("%s#%d", s.handoff[0], s.arg)
	if seen[k] || !g.treeArg[k] {
		return seen[k] // a cycle adds nothing; a non-tree parameter is untracked
	}
	seen[k] = true
	for _, use := range g.sites {
		// a section helper (constant names at the call) may index its tree
		// parameter by its own key parameters: the call site is that read
		if use.param == k && (!s.helper || !use.computed) && !g.wholeSafe(use, seen) {
			return false
		}
	}
	return true
}

func (g *guardedGate) reach(roots []string, seen map[string]bool) map[string]bool {
	for _, fn := range roots {
		if !seen[fn] {
			seen[fn] = true
			g.reach(g.calls[fn], seen)
		}
	}
	return seen
}

func (g *guardedGate) load(root string) error {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		pkgs, err := packages.Load(&packages.Config{Dir: root, Env: append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0"),
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo},
			"./cmd/...", "./internal/...", "./plugins/...")
		for _, pkg := range pkgs {
			if err == nil && len(pkg.Errors) != 0 {
				err = pkg.Errors[0]
			}
			g.info, g.fset = pkg.TypesInfo, pkg.Fset
			for _, file := range pkg.Syntax {
				rel, _ := filepath.Rel(root, pkg.Fset.Position(file.Pos()).Filename)
				g.rel = filepath.ToSlash(rel)
				ast.Inspect(file, g.visit)
			}
		}
		if err != nil {
			return fmt.Errorf("guarded config gate: %s: %w", goos, err)
		}
	}
	if len(g.roots) == 0 {
		return fmt.Errorf("guarded config gate: no GuardedFamily registration under %s", root)
	}
	return nil
}
