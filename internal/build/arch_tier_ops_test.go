package build

// Purpose: reject tier arithmetic and implicit database serialization.
// Inputs: typed production and test syntax for every supported operating system.
// Outputs: unique file:line findings, including alias and pointer operands.
// Constraints: only the canonical tier implementation and codec are exempt.
// SPORT: sensitivity boundary enforcement.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

func tierOperand(typ types.Type) bool {
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		return tierOperand(ptr.Elem())
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "github.com/acamarata/cascade/pkg/provider" && named.Obj().Name() == "SensitivityTier"
}

func tierCallArgument(call *ast.CallExpr, info *types.Info) bool {
	return slices.ContainsFunc(call.Args, func(arg ast.Expr) bool { return tierOperand(info.TypeOf(arg)) })
}

func tierUnsafeCall(call *ast.CallExpr, info *types.Info) bool {
	if !tierCallArgument(call, info) {
		return false
	}
	fun := ast.Unparen(call.Fun)
	if indexed, ok := fun.(*ast.IndexExpr); ok {
		fun = ast.Unparen(indexed.X)
	}
	switch fun := fun.(type) {
	case *ast.Ident:
		builtin, ok := info.Uses[fun].(*types.Builtin)
		return ok && (builtin.Name() == "min" || builtin.Name() == "max")
	case *ast.SelectorExpr:
		if selection := info.Selections[fun]; selection != nil {
			name := strings.TrimSuffix(selection.Obj().Name(), "Context")
			return name == "Exec" || strings.HasPrefix(name, "Query")
		}
		obj := info.Uses[fun.Sel]
		return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "cmp" && (obj.Name() == "Compare" || obj.Name() == "Less")
	}
	return false
}

// tierNumericOperators lists the ordered and arithmetic binary operators the
// gate refuses on a tier operand. A set lookup, not a switch, keeps the
// exhaustive linter off the open-ended token.Token enumeration.
var tierNumericOperators = []token.Token{token.LSS, token.GTR, token.LEQ, token.GEQ, token.ADD, token.SUB, token.MUL, token.QUO, token.REM, token.AND, token.OR, token.XOR, token.SHL, token.SHR, token.AND_NOT}

// tierTermConstraint reports whether a type-parameter constraint carries type
// terms (a union, ~T or a concrete type, directly or through an embedded
// interface). Such a constraint admits ordered or arithmetic operators in the
// generic body; comparable and method-only constraints do not.
func tierTermConstraint(constraint types.Type) bool {
	iface, ok := constraint.Underlying().(*types.Interface)
	if !ok {
		return true
	}
	for i := range iface.NumEmbeddeds() {
		embedded := iface.EmbeddedType(i)
		if _, nested := embedded.Underlying().(*types.Interface); !nested || tierTermConstraint(embedded) {
			return true
		}
	}
	return false
}

// tierGenericInstance reports an instantiation, explicit or inferred, that
// passes the tier as a type argument for a type parameter whose constraint has
// type terms: the generic body could order or convert the tier unseen. When
// the type parameters cannot be resolved, any tier type argument is reported.
func tierGenericInstance(id *ast.Ident, info *types.Info) bool {
	inst, ok := info.Instances[id]
	if !ok {
		return false
	}
	var params *types.TypeParamList
	switch obj := info.Uses[id].(type) {
	case *types.Func:
		params = obj.Origin().Signature().TypeParams()
	case *types.TypeName:
		switch generic := obj.Type().(type) {
		case *types.Named:
			params = generic.Origin().TypeParams()
		case *types.Alias:
			params = generic.Origin().TypeParams()
		}
	}
	for i := range inst.TypeArgs.Len() {
		resolved := params != nil && i < params.Len()
		if tierOperand(inst.TypeArgs.At(i)) && (!resolved || tierTermConstraint(params.At(i).Constraint())) {
			return true
		}
	}
	return false
}

func tierNumericOperator(op token.Token) bool {
	return slices.Contains(tierNumericOperators, op)
}

func tierViolation(n ast.Node, info *types.Info) bool {
	switch n := n.(type) {
	case *ast.Ident:
		return tierGenericInstance(n, info)
	case *ast.BinaryExpr:
		return tierNumericOperator(n.Op) && (tierOperand(info.TypeOf(n.X)) || tierOperand(info.TypeOf(n.Y)))
	case *ast.UnaryExpr:
		return (n.Op == token.ADD || n.Op == token.SUB || n.Op == token.XOR) && tierOperand(info.TypeOf(n.X))
	case *ast.IndexExpr:
		return !info.Types[n.Index].IsType() && tierOperand(info.TypeOf(n.Index))
	case *ast.IncDecStmt:
		return tierOperand(info.TypeOf(n.X))
	case *ast.AssignStmt:
		return n.Tok != token.ASSIGN && n.Tok != token.DEFINE && slices.ContainsFunc(append(n.Lhs[:len(n.Lhs):len(n.Lhs)], n.Rhs...), func(e ast.Expr) bool { return tierOperand(info.TypeOf(e)) })
	case *ast.CallExpr:
		if len(n.Args) == 1 && info.Types[n.Fun].IsType() && tierOperand(info.TypeOf(n.Args[0])) && !tierOperand(info.TypeOf(n.Fun)) {
			return true
		}
		return tierUnsafeCall(n, info)
	}
	return false
}

func tierFindings(pkgs []*packages.Package, root string) []string {
	found := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			path := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if rel == "pkg/provider/sensitivity.go" || rel == "pkg/provider/sensitivity_dataclass.go" {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if tierViolation(n, pkg.TypesInfo) {
					found[fmt.Sprintf("%s:%d", rel, pkg.Fset.Position(n.Pos()).Line)] = true
				}
				return true
			})
		}
	}
	return slices.Sorted(maps.Keys(found))
}

// tierCIPin is the reviewed digest of ci.yml's build-test job, the workflow's on, env
// and defaults, and ci-gate needing build-test; an edit refuses skips until re-pinned.
const tierCIPin = "79bcc97b6e1aac8ac025ce72d1894060457712710cd00bd3327e3db1671b8254"

// tierPlatforms maps each analysed GOOS to its native runner label prefix.
var tierPlatforms = map[string]string{"linux": "ubuntu-", "darwin": "macos-", "windows": "windows-"}

// tierCIShape returns the build-test job as canonical JSON and tierCIPin's digest.
func tierCIShape(ciWorkflow []byte) ([]byte, string, error) {
	var wf map[string]any
	err := yaml.Unmarshal(ciWorkflow, &wf)
	jobs, _ := wf["jobs"].(map[string]any)
	job, jobErr := json.Marshal(jobs["build-test"])
	gate, _ := jobs["ci-gate"].(map[string]any)
	needs, _ := gate["needs"].([]any) // the required aggregate must keep waiting on build-test
	scope, scopeErr := json.Marshal([]any{jobs["build-test"], wf["on"], wf["env"], wf["defaults"], slices.Contains(needs, any("build-test"))})
	return job, fmt.Sprintf("%x", sha256.Sum256(scope)), errors.Join(err, jobErr, scopeErr)
}

// tierEnv: target GOOS, no cgo, no network (GOPROXY=off); GOFLAGS is replaced.
func tierEnv(goos string) []string {
	return append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=readonly")
}

// tierPlatformLimit: runners cache only their own platform's modules, so the goos
// `go list -e -deps` listing ("importpath|module|error") may name uncached ones. It
// returns "" to load, a skip reason, or an error (a host never skips itself).
func tierPlatformLimit(goos, host, listing string, ciWorkflow []byte, pin string) (string, error) {
	var uncached []string
	for _, line := range strings.Split(listing, "\n") {
		if f := strings.Split(line, "|"); len(f) == 3 && f[1] == "" && f[2] == "module lookup disabled by GOPROXY=off" {
			uncached = append(uncached, f[0])
		}
	}
	job, digest, parseErr := tierCIShape(ciWorkflow)
	row := regexp.MustCompile(`\{"goarch":"\w+","goos":"` + goos + `","runner":"` + tierPlatforms[goos] + `[\w.-]+"\}`)
	switch {
	case len(uncached) == 0:
		return "", nil
	case goos == host || tierPlatforms[host] == "":
		return "", fmt.Errorf("GOOS=%s: %v are not in the module cache of the %s host", goos, uncached, host)
	case parseErr != nil || digest != pin:
		return "", fmt.Errorf("GOOS=%s: %v not cached on a %s host, and ci.yml's build-test job, on, env, defaults or ci-gate's needs changed: pin %s once that job still runs the unfiltered suite natively", goos, uncached, host, digest)
	case !row.Match(job) || !regexp.MustCompile(`\{"name":"[^"]*","run":"go test (-timeout \w+ )?\./\.\.\.( -v)?"\}`).Match(job) || !strings.Contains(string(job), `"runs-on":"${{ matrix.runner }}"`):
		return "", fmt.Errorf("GOOS=%s: %v not cached on a %s host and ci.yml's build-test job does not test it natively", goos, uncached, host)
	}
	return fmt.Sprintf("GOOS=%s is not type-checked on a %s host: %v are not in the module cache and the gate refuses the network (GOPROXY=off); the native build+test (%s) job in .github/workflows/ci.yml type-checks it", goos, host, uncached, goos), nil
}

func loadTierPackages(t *testing.T, root, goos, pattern string) []*packages.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  root, Env: tierEnv(goos), BuildFlags: []string{"-tags=devkeys"}, Tests: true,
	}, pattern)
	if err != nil || len(pkgs) == 0 {
		t.Fatalf("tier gate loaded %d packages: %v", len(pkgs), err)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) != 0 {
			t.Fatalf("%s: %v", pkg.PkgPath, pkg.Errors)
		}
	}
	return pkgs
}

func TestArchTierOps_RealTreeGreen(t *testing.T) {
	root := archModuleRoot(t)
	workflow, readErr := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			list := exec.Command("go", "list", "-e", "-deps", "-test", "-tags=devkeys", "-f", "{{.ImportPath}}|{{with .Module}}{{.Path}}{{end}}|{{with .Error}}{{.Err}}{{end}}", "./...")
			list.Dir, list.Env = root, tierEnv(goos)
			out, err := list.Output()
			reason, limitErr := tierPlatformLimit(goos, runtime.GOOS, string(out), workflow, tierCIPin)
			if err = errors.Join(err, readErr, limitErr); err != nil {
				t.Fatal(err)
			} else if reason != "" {
				t.Skip(reason)
			}
			if got := tierFindings(loadTierPackages(t, root, goos, "./..."), root); len(got) != 0 {
				t.Fatalf("tier operations:\n%s", strings.Join(got, "\n"))
			}
		})
	}
}

func TestTierPlatformLimit(t *testing.T) {
	const run, skip, fail, gone, cached = "false false", "true false", "false true", "|module lookup disabled by GOPROXY=off", "github.com/creack/pty|github.com/creack/pty|"
	listing := strings.Join([]string{"fmt||", cached, "github.com/godbus/dbus/v5|" + gone, "github.com/creack/pty|github.com/creack/pty" + gone, "example.com/x||no required module provides package example.com/x"}, "\n")
	job := "on: [push]\njobs:\n  build-test:\n    runs-on: ${{ matrix.runner }}\n    strategy: {matrix: {include: [{goos: darwin, goarch: arm64, runner: macos-15}, {goos: linux, goarch: amd64, runner: %s}]}}\n    steps:\n      - name: go test ./...\n%s        run: %s\n  conformance:\n    strategy: {matrix: {include: [{goos: windows, goarch: amd64, runner: windows-latest}]}}\n  ci-gate:\n    needs: [build-test]\n"
	ok := fmt.Sprintf(job, "ubuntu-latest", "", "go test -timeout 25m ./... -v")
	for i, tc := range []struct{ goos, host, listing, ci, want string }{{"linux", "darwin", cached, ok, run}, {"darwin", "darwin", listing, ok, fail}, {"linux", "darwin", listing, ok, skip},
		{"windows", "darwin", listing, ok, fail}, {"linux", "freebsd", listing, ok, fail}, {"linux", "darwin", listing, fmt.Sprintf(job, "macos-15", "", "go test ./..."), fail},
		{"linux", "darwin", listing, fmt.Sprintf(job, "ubuntu-latest", "        shell: bash\n", "go test ./..."), fail}, {"linux", "darwin", listing, fmt.Sprintf(job, "ubuntu-latest", "", "go test -run X ./..."), fail}, {"linux", "darwin", listing, strings.Replace(ok, "${{ matrix.runner }}", "macos-15", 1), fail}, {"linux", "darwin", listing, "jobs: [", fail}, {"linux", "darwin", listing, ok + "env: {GOFLAGS: -run=X}\n", fail}, {"linux", "darwin", listing, strings.Replace(ok, "needs: [build-test]", "needs: [lint]", 1), fail}} {
		_, pin, _ := tierCIShape([]byte(map[bool]string{true: ok, false: tc.ci}[i >= 10])) // cases 10-11: a stale pin
		reason, err := tierPlatformLimit(tc.goos, tc.host, tc.listing, []byte(tc.ci), pin)
		if fmt.Sprint(reason != "", err != nil) != tc.want || (tc.want == skip && !strings.Contains(reason, "[github.com/godbus/dbus/v5]")) {
			t.Errorf("case %d: reason %q, err %v", i, reason, err)
		}
	}
}

func TestArchTierOps_SeededViolationRed(t *testing.T) {
	root := archModuleRoot(t)
	pkgs := loadTierPackages(t, root, "linux", "./internal/build/testdata/seeded-violations/tier-ops")
	got := tierFindings(pkgs, root)
	var want []string
	for _, name := range []string{"generics.go", "violations.go", "violations_test.go"} {
		path := "internal/build/testdata/seeded-violations/tier-ops/" + name
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "// want tier violation") {
				want = append(want, fmt.Sprintf("%s:%d", path, i+1))
			}
		}
	}
	sort.Strings(want)
	if len(want) != 52 {
		t.Fatalf("fixture has %d cases, want 52", len(want))
	}
	t.Log(strings.Join(got, "\n"))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings = %v; want exactly %v", got, want)
	}
}
