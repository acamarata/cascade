//go:build !windows

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type repairRig struct {
	w        *daemonWiring
	paths    runtime.PathProvider
	registry *rpc.Registry
	manifest *daemon.Manifest
	store    provider.Store
	db       *sql.DB
	journal  *repo.WatchJournal
	cli      contextScopeDeps
}

// composeRepoWatch exercises the real daemon registry and supervised loop.
func composeRepoWatch(t *testing.T) *repairRig {
	t.Helper()
	factsTestEnv(t)
	old := repoWatchRelist
	repoWatchRelist = time.Hour
	t.Cleanup(func() { repoWatchRelist = old })
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	rig := &repairRig{paths: deps.Paths}
	captureRepairWiring(t, rig)
	opts, cleanups, err := composeDaemon(t.Context(), deps, func(reg *rpc.Registry, _ *rpc.SSEMux) { rig.registry = reg })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	rig.manifest = opts.Manifest
	rig.store, rig.db = rig.w.Store, rig.w.Deps.RawDB
	rig.journal, err = repo.NewWatchJournal(rig.db)
	if err != nil {
		t.Fatal(err)
	}
	rig.cli = contextScopeDeps{Paths: rig.paths, DialContext: redirectDial(client.UnixDialer, serveRegistry(t, rig.registry))}
	return rig
}

func captureRepairWiring(t *testing.T, rig *repairRig) {
	t.Helper()
	for i := range daemonRegistrations {
		if daemonRegistrations[i].Name != "repo-watch" {
			continue
		}
		wire := daemonRegistrations[i].Wire
		daemonRegistrations[i].Wire = func(w *daemonWiring) error { rig.w = w; return wire(w) }
		t.Cleanup(func() { daemonRegistrations[i].Wire = wire })
		return
	}
	t.Fatal("missing repo-watch composition")
}

func (r *repairRig) command(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newContextCmd(r.cli)
	cmd.PersistentFlags().Bool("json", true, "")
	cmd.SetArgs(append([]string{"repair"}, args...))
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func (r *repairRig) pathsFor(t *testing.T, id string) map[string]repo.PathState {
	t.Helper()
	paths, err := r.journal.Paths(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func (r *repairRig) auditRows(t *testing.T) []audit.Record {
	t.Helper()
	log, err := openDaemonAuditLog(r.store, runtime.SystemClock{}, nil, r.paths)
	if err != nil {
		t.Fatal(err)
	}
	page, err := log.Query(t.Context(), audit.Filter{Kinds: []audit.Kind{audit.KindConfigReload}})
	if err != nil {
		t.Fatal(err)
	}
	return page.Records
}

func (r *repairRig) search(t *testing.T, id string) []retrieval.Hit {
	t.Helper()
	idx, err := retrieval.NewIndex(r.store)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, path := range r.pathsFor(t, id) {
		ids = append(ids, path.ChunkIDs...)
	}
	hits, err := idx.Search(t.Context(), retrieval.SearchRequest{Text: "repairtoken", Scope: fusion.ScopePredicate{CorpusIDs: []string{corpus.CorpusIDCode}, RecordIDs: ids}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("fixture token was not indexed")
	}
	return hits
}

func repairFixture(t *testing.T, r *repairRig) (string, string) {
	t.Helper()
	root := factsGitRepo(t, map[string]string{"first.md": "# First\n\nrepairtoken original content\n"})
	a, admissionErr := repo.Admit(t.Context(), root, "first.md")
	if admissionErr != nil || !a.Admitted {
		t.Fatalf("fixture admission: %+v %v", a, admissionErr)
	}
	out, err := r.command(t, "--repo", root)
	if err != nil || !strings.Contains(strings.ReplaceAll(out, " ", ""), `"repaired":1`) {
		t.Fatalf("initial sweep: %s %v", out, err)
	}
	inv, err := registerRepository(t.Context(), r.paths, runtime.SystemClock{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.pathsFor(t, inv.Repository.ID)["first.md"]; !ok {
		t.Fatal("first.md is not journaled")
	}
	return root, inv.Repository.ID
}

func TestContextRepairSweepReportsRepaired(t *testing.T) {
	r := composeRepoWatch(t)
	root, id := repairFixture(t, r)
	running := false
	for _, state := range r.manifest.Snapshot() {
		if state.Name == "repo-watch:"+id && state.State == daemon.SubsystemRunning {
			running = true
		}
	}
	if !running {
		t.Fatal("repair did not immediately start the watcher")
	}
	out, err := r.command(t, "--repo", root)
	if err != nil || !strings.Contains(strings.ReplaceAll(out, " ", ""), `"repaired":0`) {
		t.Fatalf("second sweep: %s %v", out, err)
	}
}

func TestContextRepairRebuildAudited(t *testing.T) {
	r := composeRepoWatch(t)
	root, id := repairFixture(t, r)
	before := r.pathsFor(t, id)
	old := openContextRepairAudit
	t.Cleanup(func() { openContextRepairAudit = old })
	calls := 0
	openContextRepairAudit = func(w *daemonWiring) (contextRepairAppend, error) {
		appendEvent, err := old(w)
		return func(ctx context.Context, event audit.Event) (audit.Record, error) {
			calls++
			if !reflect.DeepEqual(before, r.pathsFor(t, id)) {
				t.Error("journal discarded before audit append")
			}
			return appendEvent(ctx, event)
		}, err
	}
	out, err := r.command(t, "--repo", root, "--rebuild", "--reason", "x")
	if err != nil || !strings.Contains(strings.ReplaceAll(out, " ", ""), `"repaired":1`) {
		t.Fatalf("rebuild: %s %v", out, err)
	}
	rows := r.auditRows(t)
	if calls != 1 || len(rows) != 1 {
		t.Fatalf("append calls %d; audit rows %+v", calls, rows)
	}
	row := rows[0]
	var explain map[string]string
	if err := json.Unmarshal(row.Explain, &explain); err != nil {
		t.Fatal(err)
	}
	if row.Action != "context.repair.rebuild" || row.Actor != "local-owner" ||
		!reflect.DeepEqual(explain, map[string]string{"repo_id": id, "reason": "x"}) {
		t.Fatalf("audit: %+v %v", row, explain)
	}
	if _, ok := r.pathsFor(t, id)["first.md"]; !ok {
		t.Fatal("first.md not rebuilt")
	}
}

func TestContextRepairAuditFailureDiscardsNothing(t *testing.T) {
	r := composeRepoWatch(t)
	root, id := repairFixture(t, r)
	before, hits := r.pathsFor(t, id), r.search(t, id)
	old := openContextRepairAudit
	t.Cleanup(func() { openContextRepairAudit = old })
	failure := cascade.New(cascade.KindUnavailable, "audit offline")
	for _, mode := range []string{"open", "nil", "append"} {
		t.Run(mode, func(t *testing.T) {
			openContextRepairAudit = func(*daemonWiring) (contextRepairAppend, error) {
				if mode == "open" {
					return nil, failure
				}
				if mode == "nil" {
					return nil, nil
				}
				return func(context.Context, audit.Event) (audit.Record, error) { return audit.Record{}, failure }, nil
			}
			_, err := r.command(t, "--repo", root, "--rebuild", "--reason", "x")
			if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "audit") {
				t.Fatalf("failure: %v", err)
			}
			if !reflect.DeepEqual(before, r.pathsFor(t, id)) {
				t.Fatal("audit failure discarded journal")
			}
			if !reflect.DeepEqual(hits, r.search(t, id)) {
				t.Fatal("audit failure discarded indexed chunks")
			}
		})
	}
	if len(r.auditRows(t)) != 0 {
		t.Fatal("failed rebuild appended an audit record")
	}
}

func TestContextRepairRebuildGoModule(t *testing.T) {
	r := composeRepoWatch(t)
	root := factsGitRepo(t, map[string]string{"go.mod": "module example.com/repair\n\ngo 1.22\n", "main.go": "package main\nfunc main() {}\n"})
	out, err := r.command(t, "--repo", root, "--rebuild", "--reason", "x")
	if err != nil {
		t.Fatalf("rebuild module: %s %v", out, err)
	}
	inv, err := registerRepository(t.Context(), r.paths, runtime.SystemClock{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.pathsFor(t, inv.Repository.ID)["main.go"]; !ok {
		t.Fatal("main.go not rebuilt")
	}
	for _, state := range r.manifest.Snapshot() {
		if strings.HasPrefix(state.Name, "repo-watch") && state.State == daemon.SubsystemError {
			t.Fatalf("watcher failed: %+v", state)
		}
	}
}

func TestContextRepairRefusesEmbedded(t *testing.T) {
	paths := factsTestEnv(t)
	calls := 0
	cmd := newContextCmd(contextScopeDeps{Paths: paths, DialContext: countRepairDials(client.UnixDialer, &calls)})
	cmd.SetArgs([]string{"repair", "--repo", filepath.Join(t.TempDir(), "repository")})
	cmd.SetContext(runtime.WithDaemonlessState(t.Context(), runtime.DaemonlessState{Embedded: true}))
	err := cmd.Execute()
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "cascade daemon start") || calls != 0 {
		t.Fatalf("embedded: %v, dials %d", err, calls)
	}
}

func countRepairDials[C any](dial func(context.Context, string) (C, error), calls *int) func(context.Context, string) (C, error) {
	return func(ctx context.Context, path string) (C, error) { *calls++; return dial(ctx, path) }
}
