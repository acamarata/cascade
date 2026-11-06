//go:build !windows

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/generate"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestContextGenerateRegistersRepository(t *testing.T) {
	factsTestEnv(t)
	deps := policyDeps(t, "")
	paths := deps.Paths
	root := factsGitRepo(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n", "main.go": "package main\n"})
	var reg *rpc.Registry
	_, cleanups, err := composeDaemon(t.Context(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"repo": root, "check": true})
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "context.generate", Params: raw}); eo != nil {
		t.Fatal(eo)
	}
	err = withRepoDB(ctx, paths, runtime.NewSystemClock(), func(db *sql.DB) error {
		canonical, err := canonicalRepoRoot(ctx, root)
		if err != nil {
			return err
		}
		rec, ok, err := scope.NewGraphStore(db).RepositoryForRoot(ctx, canonical)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("context.generate did not register the repository")
		}
		inv, ok, err := repo.NewStore(db).Get(ctx, rec.ID)
		if err == nil && (!ok || inv.Repository.ID != rec.ID) {
			t.Fatal("repository has no stored inventory")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContextGenerateRPCRequiresRepo(t *testing.T) {
	paths := factsTestEnv(t)
	root := factsGitRepo(t, map[string]string{"go.mod": "module example.com/x\n"})
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	d := contextGenerateDeps{Paths: paths, Clock: runtime.NewSystemClock()}
	for _, raw := range []string{`{}`, `null`, `{"repo":"relative"}`, `{"repo":"/","unknown":true}`, `{"repo":"/","check":"yes"}`, `{} {}`} {
		_, err := d.handle(t.Context(), json.RawMessage(raw))
		wantFactsKind(t, err, cascade.KindInvalidInput, "context.generate:")
	}
	raw, _ := json.Marshal(generateParams{Repo: sub, Check: true})
	_, err := d.handle(t.Context(), raw)
	wantFactsKind(t, err, cascade.KindInvalidInput, "top-level")
	raw, _ = json.Marshal(generateParams{Repo: root, Check: true})
	out, err := d.handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	drift := out.(map[string]any)["drift"].(generate.Drift)
	if drift.Clean() {
		t.Fatal("fresh repository should report absent manifest")
	}
	if _, err := os.Stat(filepath.Join(root, generate.ManifestRel)); !os.IsNotExist(err) {
		t.Fatalf("Check wrote manifest: %v", err)
	}
}

func TestBuildRPCServerRegistersContextGenerate(t *testing.T) {
	factsTestEnv(t)
	reg := composeForTest(t).Registry
	reg.SetAccessPolicy(nil)
	if !strings.Contains(strings.Join(reg.Methods(), "\n"), contextGenerateMethod) {
		t.Fatal("composed daemon missing context.generate")
	}
	for _, caller := range []principal.Caller{{}, {OwnerCLI: true}, {Principal: &principal.Principal{ID: "session", Kind: principal.KindSession, Scope: "project:x"}}} {
		ctx := principal.WithCaller(t.Context(), caller)
		_, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: contextGenerateMethod, Params: json.RawMessage(`{}`)})
		if eo == nil {
			t.Fatal("invalid call succeeded")
		}
		denied := eo.Code == cascade.NewRPCError(cascade.New(cascade.KindPermissionDenied, "x")).Code
		if denied != (caller.Principal != nil) {
			t.Fatalf("caller %+v: %+v", caller, eo)
		}
	}
	wiring := buildMCPToolWiring(t.Context(), doctorTestPaths(t), runtime.SystemClock{})
	t.Cleanup(wiring.Close)
	for _, tool := range daemonMCPToolRegistry(wiring.Methods, grantEverything{}).List() {
		if tool.Name == contextGenerateMethod || tool.Name == "cascade_context_generate" {
			t.Fatal("generate exposed through MCP")
		}
	}
}

// generateReplayProbe replays the recorded help and version without a process.
func generateReplayProbe(t *testing.T) generate.WorkflowsSupportProbe {
	t.Helper()
	b, err := os.ReadFile("../../internal/context/generate/testdata/real-cc-workflows/unsupported-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version, Stdout string
		ExitCode        int `json:"exit_code"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &generate.ExecProbe{
		LookPath: func(string) (string, error) { return "recorded-harness", nil },
		Run: func(_ context.Context, _ string, args, _ []string) ([]byte, int, error) {
			if args[0] == "--version" {
				return []byte(f.Version), 0, nil
			}
			return []byte(f.Stdout), f.ExitCode, nil
		},
	}
}

func TestContextGenerateAttentionSinkBound(t *testing.T) {
	paths := factsTestEnv(t)
	root := factsGitRepo(t, map[string]string{"go.mod": "module example.com/x\n", "AGENTS.md": "operator text\n"})
	clock, kv := runtime.NewSystemClock(), storetest.NewMemStore()
	d := productionContextGenerateDeps(&daemonWiring{Paths: paths, Clock: clock, Store: kv})
	d.Probe = generateReplayProbe(t)
	raw, _ := json.Marshal(generateParams{Repo: root})
	out, err := d.handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(generate.Result)
	if len(res.Refused) != 1 || res.Refused[0].Path != "AGENTS.md" {
		t.Fatalf("refusals: %+v", res.Refused)
	}
	inv, err := registerRepository(t.Context(), paths, clock, root)
	if err != nil {
		t.Fatal(err)
	}
	ref := scope.Ref{Kind: scope.ScopeKindProject, ID: inv.Repository.ID}
	queue := supervision.NewStore(kv, clock, nil, supervision.NewSystemIDGenerator(), 0)
	items, err := queue.ListInScopes(t.Context(), []scope.Ref{ref}, supervision.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != supervision.KindError || items[0].SourceRef != "context.generate:AGENTS.md" || items[0].ScopeRef != ref {
		t.Fatalf("supervision items: %+v", items)
	}
	b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil || string(b) != "operator text\n" {
		t.Fatalf("refused file changed: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md.cascade-new")); err != nil {
		t.Fatal(err)
	}
}
