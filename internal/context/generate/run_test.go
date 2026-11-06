package generate

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stubInventory is the inventory the stubbed Register returns.
func stubInventory() repo.Inventory {
	return repo.Inventory{
		Repository: repo.RepositoryRef{ID: "repo-run-stub"},
		Languages: []repo.LanguageFacts{{Language: repo.LanguageGo, Detected: true, Evidence: []string{"go.mod"},
			Commands: repo.Commands{Build: "go build ./...", Test: "go test ./...", Lint: "go vet ./..."}}},
	}
}

// manifestProbe wraps a probe and fails the test when the manifest exists
// by the time the workflows leg runs.
type manifestProbe struct {
	t    *testing.T
	root string
	in   WorkflowsSupportProbe
}

func (m manifestProbe) Probe(ctx context.Context) (ProbeResult, error) {
	if !missing(m.root, ManifestRel) {
		m.t.Error("the manifest was written before the workflows leg")
	}
	return m.in.Probe(ctx)
}

// stubDeps returns RunDeps over a stubbed Register and the given probe.
func stubDeps(rec *recorder, registered *int, p WorkflowsSupportProbe) RunDeps {
	return RunDeps{
		Clock:     runtime.NewFixedClock(fixedNow),
		Attention: rec.sink(),
		Register: func(context.Context, string) (repo.Inventory, error) {
			*registered++
			return stubInventory(), nil
		},
		Facts: &sourceDouble{},
		Probe: p,
	}
}

// legPaths renders the four legs of the stub view, in order.
func legPaths(t *testing.T, root string, p WorkflowsSupportProbe) []string {
	t.Helper()
	v, err := BuildView(context.Background(), stubInventory(), root, &sourceDouble{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err1 := RenderRules(v)
	agents, err2 := RenderAgentsSkills(v)
	wf, err3 := RenderWorkflows(context.Background(), v, p)
	scoped, err4 := RenderScopedHarness(v)
	for _, err := range []error{err1, err2, err3, err4} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	for _, leg := range [][]Rendered{rules, agents, wf.Files, scoped} {
		if len(leg) == 0 {
			t.Fatal("a leg rendered nothing; the order is not exercised")
		}
		for _, r := range leg {
			out = append(out, r.Path)
		}
	}
	return out
}

func TestRunLegOrder(t *testing.T) {
	root, _ := newRepo(t)
	var calls []runCall
	rec, registered := &recorder{}, 0
	probe := manifestProbe{t: t, root: root, in: fixtureProbe(t, "supported-fixture.json", &calls)}
	start := time.Now().Add(-time.Second)
	res, err := Run(context.Background(), root, stubDeps(rec, &registered, probe))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := legPaths(t, t.TempDir(), fixtureProbe(t, "supported-fixture.json", &calls)); !reflect.DeepEqual(res.Written, want) {
		t.Fatalf("Written = %v\nwant leg order %v", res.Written, want)
	}
	if registered != 1 || res.Deferral != nil || len(res.Adopted) != 0 || len(res.Refused) != 0 || len(rec.calls) != 0 {
		t.Fatalf("registered %d, result %+v, sink %+v", registered, res, rec.calls)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(ManifestRel)))
	if err != nil || info.ModTime().Before(start) {
		t.Fatalf("manifest not written by this Run: %v %v", info, err)
	}
	m, ok, err := ReadManifest(root)
	if err != nil || !ok || m.GeneratedAt != fixedNow.Format(time.RFC3339) {
		t.Fatalf("manifest = %+v %v %v", m, ok, err)
	}
	var paths []string
	for _, e := range m.Entries {
		paths = append(paths, e.Path)
	}
	want := append([]string(nil), res.Written...)
	sort.Strings(want)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("manifest paths = %v, want %v", paths, want)
	}
}

func TestRunNilDepsRefused(t *testing.T) {
	cases := map[string]func(*RunDeps){
		"Clock":     func(d *RunDeps) { d.Clock = nil },
		"Attention": func(d *RunDeps) { d.Attention = nil },
		"Register":  func(d *RunDeps) { d.Register = nil },
		"Facts":     func(d *RunDeps) { d.Facts = nil },
		"Probe":     func(d *RunDeps) { d.Probe = nil },
	}
	for field, clear := range cases {
		t.Run(field, func(t *testing.T) {
			root, _ := newRepo(t)
			registered := 0
			d := stubDeps(&recorder{}, &registered, &stubProbe{})
			clear(&d)
			_, err := Run(context.Background(), root, d)
			wantKind(t, err, cascade.KindInvalidInput, "generate: nil RunDeps."+field)
			if entries, _ := os.ReadDir(root); len(entries) != 0 || registered != 0 {
				t.Fatalf("a refused Run wrote %d entries, registered %d", len(entries), registered)
			}
		})
	}
	registered := 0
	_, err := Run(context.Background(), "", stubDeps(&recorder{}, &registered, &stubProbe{}))
	wantKind(t, err, cascade.KindInvalidInput, "generate: empty repoRoot")
	if registered != 0 {
		t.Fatal("an empty repoRoot reached Register")
	}
}

// contractFields is outputs[0]: each struct's fields, in order, as
// "Name Type".
var contractFields = map[string][]string{
	"RunDeps": {
		"Clock runtime.Clock",
		"Attention AttentionSink",
		"Register func(ctx context.Context, root string) (repo.Inventory, error)",
		"Facts FactSource",
		"Probe WorkflowsSupportProbe",
	},
	"Result": {"Written []string", "Adopted []string", "Refused []Outcome", "Deferral *Deferral"},
}

// contractRun is the exact Run signature.
const contractRun = "func(ctx context.Context, repoRoot string, d RunDeps) (Result, error)"

func TestRunContractShape(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	var exported []string
	runSig := ""
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				exported = append(exported, specExports(spec, got)...)
			}
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				exported = append(exported, d.Name.Name)
				runSig = types.ExprString(d.Type)
			}
		}
	}
	sort.Strings(exported)
	if !reflect.DeepEqual(exported, []string{"Result", "Run", "RunDeps"}) {
		t.Fatalf("run.go exports %v, want exactly Result, Run, RunDeps", exported)
	}
	if !reflect.DeepEqual(got, contractFields) {
		t.Fatalf("struct fields = %v\nwant %v", got, contractFields)
	}
	if runSig != contractRun {
		t.Fatalf("Run = %s, want %s", runSig, contractRun)
	}
}

// specExports returns the exported names spec declares and records each
// struct type's fields in fields.
func specExports(spec ast.Spec, fields map[string][]string) []string {
	var out []string
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if !s.Name.IsExported() {
			return nil
		}
		out = append(out, s.Name.Name)
		if st, ok := s.Type.(*ast.StructType); ok {
			for _, fl := range st.Fields.List {
				for _, n := range fl.Names {
					fields[s.Name.Name] = append(fields[s.Name.Name], n.Name+" "+types.ExprString(fl.Type))
				}
			}
		}
	case *ast.ValueSpec:
		for _, n := range s.Names {
			if n.IsExported() {
				out = append(out, n.Name)
			}
		}
	}
	return out
}
