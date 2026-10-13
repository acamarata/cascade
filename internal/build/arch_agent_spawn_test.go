package build

// Purpose: the agent-spawn arch gates on the real tree (green, each with a
// non-vacuity probe) and on seeded fixture modules (red).
// SPORT: internal.build tests (EXTEND) - P1-AGT-02.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/agents"
)

// ScanConformanceImporters reports a non-test importer of conformance (M-8).
func ScanConformanceImporters(root string) ([]AgentSpawnViolation, error) {
	return agentImportScan(root, func(rel, imp string) string {
		if strings.HasPrefix(imp, agentConformancePk) && !strings.HasPrefix(rel, "providers/agents/conformance/") {
			return "non-test importer of the conformance suite"
		}
		return ""
	})
}

// spawnStub is the fixture module's pkg/provider and one driver.
var spawnStub = map[string]string{
	"go.mod": "module " + cascadeModulePath + "\n\ngo 1.21\n",
	"pkg/provider/p.go": `package provider

import "context"

type SpawnEnvelope struct {
	Digest, Principal               string
	Roots, Tools, Commands, Egress []string
	Sensitivity, ExpiresAt          int
}
type AgentJobSpec struct{ Envelope SpawnEnvelope }
type SpawnResult struct{}
type AgentProvider interface {
	Spawn(context.Context, AgentJobSpec) (SpawnResult, error)
}
`,
	"providers/agents/claude/d.go": "package claude\n\nimport (\n\t\"context\"\n\n\t\"" + cascadeModulePath + "/pkg/provider\"\n)\n\ntype Driver struct{}\n\n" +
		"func (Driver) Spawn(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {\n\treturn provider.SpawnResult{}, nil\n}\n",
	"providers/agents/conformance/s.go":  spawnBody("conformance", "p.Spawn(ctx, provider.AgentJobSpec{})"),
	"internal/providers/agents/spawn.go": spawnBody("agents", "p.Spawn(ctx, provider.AgentJobSpec{})"),
	"internal/providers/agents/use.go":   "package agents\n\nimport _ \"" + cascadeModulePath + "/providers/agents/claude\"\n",
	"internal/ok/ok.go":                  "package ok\n\nimport \"os/exec\"\n\nfunc F() { _ = exec.Command(\"git\", \"status\") }\n",
}

// spawnBody is a package whose F calls expr with p an AgentProvider.
func spawnBody(pkg, expr string) string {
	return "package " + pkg + "\n\nimport (\n\t\"context\"\n\n\t\"" + cascadeModulePath + "/pkg/provider\"\n)\n\n" +
		"func F(ctx context.Context, p provider.AgentProvider) {\n\t_, _ = " + expr + "\n}\n"
}

// spawnFixture writes the stub module plus extra files and returns its root.
func spawnFixture(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for _, files := range []map[string]string{spawnStub, extra} {
		for dest, body := range files {
			full := filepath.Join(root, filepath.FromSlash(dest))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// agentWhere renders violations as "file:line what" lines.
func agentWhere(v []AgentSpawnViolation) []string {
	var out []string
	for _, x := range v {
		out = append(out, x.File+": "+x.What)
	}
	return out
}

// expect fails unless the scan found nothing (file "") or only violations in file.
func expect(t *testing.T, name string, v []AgentSpawnViolation, err error, file string) {
	t.Helper()
	if err != nil || (file == "") != (len(v) == 0) {
		t.Fatalf("%s: violations %v, error %v, want %q", name, agentWhere(v), err, file)
	}
	for _, x := range v {
		if x.File != file || x.Line < 1 || x.What == "" {
			t.Fatalf("%s: unexpected violation %+v (want only %s)", name, x, file)
		}
	}
}

// importsOf reports every non-test importer of an import path with prefix pre.
func importsOf(root, pre string) ([]AgentSpawnViolation, error) {
	return agentImportScan(root, func(_, imp string) string { return map[bool]string{true: "imports"}[strings.HasPrefix(imp, pre)] })
}

func TestArchAgentDriverImporters_RealTree(t *testing.T) {
	root := archModuleRoot(t)
	v, err := ScanAgentDriverImporters(root)
	expect(t, "real tree", v, err, "")
	// Non-vacuity: the walk sees the shim's own driver import.
	all, err := importsOf(root, agentDriverPrefix)
	if err != nil || !slices.ContainsFunc(all, func(x AgentSpawnViolation) bool { return x.File == "internal/providers/agents/harness_claude.go" }) {
		t.Fatalf("the importer walk did not see harness_claude.go: %v, %v", agentWhere(all), err)
	}
	v, err = ScanAgentDriverImporters(spawnFixture(t, nil))
	expect(t, "fixture with only the shim importing a driver", v, err, "")
}

func TestArchAgentDriverImporters_SeededViolationRed(t *testing.T) {
	imp := func(path string) string { return "package scratch\n\nimport _ \"" + cascadeModulePath + path + "\"\n" }
	for dest, body := range map[string]string{
		"internal/scratch/imp.go":            imp("/providers/agents/claude"),
		"cmd/scratch/main.go":                imp("/Providers/Agents/Claude"),
		"providers/agents/other/o.go":        imp("/providers/agents/claude"),
		"internal/providers/agents/sub/s.go": imp("/providers/agents/claude"),
	} {
		v, err := ScanAgentDriverImporters(spawnFixture(t, map[string]string{dest: body}))
		expect(t, dest, v, err, dest)
	}
}

func TestArchSpawnCallers_RealTree(t *testing.T) {
	root := archModuleRoot(t)
	if _, err := os.Stat(filepath.Join(root, "providers/agents/conformance/suite.go")); err != nil {
		t.Fatalf("conformance must be present for this gate: %v", err)
	}
	v, err := ScanSpawnCallers(root)
	expect(t, "real tree", v, err, "")
	// Non-vacuity: with the allowance switched off the scan sees the suite's calls.
	v, err = scanSpawn(root, func(string) bool { return false })
	if err != nil || len(v) == 0 || !strings.HasPrefix(v[0].File, "providers/agents/conformance/") {
		t.Fatalf("an unallowed scan of the real tree found %v (error %v), want the conformance Spawn calls", agentWhere(v), err)
	}
	v, err = ScanSpawnCallers(spawnFixture(t, nil))
	expect(t, "fixture with only allowed callers", v, err, "")
}

func TestArchSpawnCallers_SeededViolationRed(t *testing.T) {
	const spec = "provider.AgentJobSpec{}"
	generic := "package bad\n\nimport (\n\t\"context\"\n\n\t\"" + cascadeModulePath + "/pkg/provider\"\n)\n\n" +
		"type spawner[S, R any] interface{ Spawn(context.Context, S) (R, error) }\n\n" +
		"func run[S, R any, T spawner[S, R]](ctx context.Context, t T, s S) (R, error) { return t.Spawn(ctx, s) }\n\n" +
		"func F(ctx context.Context, p provider.AgentProvider) { _, _ = run[provider.AgentJobSpec, provider.SpawnResult](ctx, p, " + spec + ") }\n"
	embedded := "package bad\n\nimport (\n\t\"context\"\n\n\t\"" + cascadeModulePath + "/pkg/provider\"\n)\n\ntype W struct{ provider.AgentProvider }\n\n" +
		"func F(ctx context.Context, w W) { _, _ = w.Spawn(ctx, " + spec + ") }\n"
	concrete := "package bad\n\nimport (\n\t\"context\"\n\n\t\"" + cascadeModulePath + "/pkg/provider\"\n\t\"" + cascadeModulePath +
		"/providers/agents/claude\"\n)\n\nfunc F(ctx context.Context) { _, _ = claude.Driver{}.Spawn(ctx, " + spec + ") }\n"
	for name, body := range map[string]string{
		"call":                spawnBody("bad", "p.Spawn(ctx, "+spec+")"),
		"method value":        spawnBody("bad", "func() (provider.SpawnResult, error) { f := p.Spawn; return f(ctx, "+spec+") }()"),
		"method expression":   spawnBody("bad", "provider.AgentProvider.Spawn(p, ctx, "+spec+")"),
		"anonymous interface": spawnBody("bad", "any(p).(interface {\n\t\tSpawn(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error)\n\t}).Spawn(ctx, "+spec+")"),
		"concrete driver":     concrete, "embedded": embedded, "generic constraint": generic,
	} {
		dest := "internal/scratch/bad.go"
		v, err := ScanSpawnCallers(spawnFixture(t, map[string]string{dest: body}))
		expect(t, name, v, err, dest)
	}
}

func TestArchConformanceHasNoNonTestImporter_RealTree(t *testing.T) {
	root := archModuleRoot(t)
	v, err := ScanConformanceImporters(root)
	expect(t, "real tree", v, err, "")
	if all, err := importsOf(root, agentDriverPrefix); err != nil || len(all) == 0 {
		t.Fatalf("the importer walk saw no providers/agents import: %v", err)
	}
	test := map[string]string{"internal/scratch/t_test.go": "package scratch\n\nimport _ \"" + cascadeModulePath + "/providers/agents/conformance\"\n"}
	v, err = ScanConformanceImporters(spawnFixture(t, test))
	expect(t, "a test-file importer", v, err, "")
}

func TestArchConformanceHasNoNonTestImporter_SeededViolationRed(t *testing.T) {
	dest := "internal/scratch/imp.go"
	v, err := ScanConformanceImporters(spawnFixture(t, map[string]string{dest: "package scratch\n\nimport _ \"" + cascadeModulePath + "/providers/agents/conformance\"\n"}))
	expect(t, dest, v, err, dest)
}

// execBinaries: the registered vendor basenames plus the known harness names.
func execBinaries() []string {
	out := []string{"claude", "codex", "opencode", "antigravity", "agy"}
	for _, b := range agents.Bindings() {
		out = append(out, b.Binaries...)
	}
	return slices.Compact(slices.Sorted(slices.Values(out)))
}

func TestArchHarnessExec_RealTree(t *testing.T) {
	bins, root := execBinaries(), archModuleRoot(t)
	if !slices.Contains(bins, "claude") {
		t.Fatalf("registered binaries %v lack claude", bins)
	}
	v, err := ScanHarnessExec(root, bins)
	expect(t, "real tree", v, err, "")
	// Non-vacuity: the scan sees real exec.Command("git", ...) calls outside providers/agents.
	if v, err = ScanHarnessExec(root, []string{"git"}); err != nil || len(v) == 0 {
		t.Fatalf("a scan for git found %v (error %v), want the tree's git invocations", agentWhere(v), err)
	}
	v, err = ScanHarnessExec(spawnFixture(t, nil), bins)
	expect(t, "fixture exec of git", v, err, "")
}

func TestArchHarnessExec_SeededViolationRed(t *testing.T) {
	cmd := func(imports, body string) string {
		return "package bad\n\nimport (\n" + imports + ")\n\n" + body + "\n"
	}
	for name, body := range map[string]string{
		"literal":        cmd("\t\"os/exec\"\n", "func F() { _ = exec.Command(\"codex\", \"--x\") }"),
		"constant":       cmd("\t\"context\"\n\t\"os/exec\"\n", "const bin = \"/usr/local/bin/opencode\"\n\nfunc F(ctx context.Context) { _ = exec.CommandContext(ctx, bin) }"),
		"windows path":   cmd("\t\"os/exec\"\n", "func F() { _ = exec.Command(`C:\\tools\\claude.exe`) }"),
		"import alias":   cmd("\tx \"os/exec\"\n", "func F() { _ = x.Command(\"claude\") }"),
		"local constant": cmd("\t\"os/exec\"\n", "func F() {\n\tconst bin = \"claude\"\n\t_ = exec.Command(bin)\n}"),
		"conversion":     cmd("\t\"os/exec\"\n", "func F() { _ = exec.Command(string(\"claude\")) }"),
		"dot import":     cmd("\t. \"os/exec\"\n", "func F() { _ = Command(\"claude\") }"),
		"function value": cmd("\t\"os/exec\"\n", "var run = exec.Command"),
		"windows shim":   cmd("\t\"os/exec\"\n", "func F() { _ = exec.Command(\"claude.cmd\") }"),
		"other case":     cmd("\t\"os/exec\"\n", "func F() { _ = exec.Command(\"Claude\") }"),
	} {
		dest := "internal/scratch/bad.go"
		v, err := ScanHarnessExec(spawnFixture(t, map[string]string{dest: body}), execBinaries())
		expect(t, name, v, err, dest)
	}
	// Inside providers/agents the same call is the driver's own and passes.
	ok := strings.Replace(cmd("\t\"os/exec\"\n", "func G() { _ = exec.Command(\"claude\") }"), "package bad", "package claude", 1)
	v, err := ScanHarnessExec(spawnFixture(t, map[string]string{"providers/agents/claude/e.go": ok}), execBinaries())
	expect(t, "exec inside providers/agents", v, err, "")
}
