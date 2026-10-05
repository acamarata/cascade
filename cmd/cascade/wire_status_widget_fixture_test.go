//go:build !windows

// Purpose: the second half of the status-widget registration tests (a split
//
//	forced by the 300-line cap, wire_status_widget_test.go): the fixture
//	regenerated from the evidence path, and the check that the wire file is the
//	only caller of the refresh loop.
//
// Inputs: widgetWire (wire_status_widget_test.go).
// Constraints: the fixture is rewritten only under CASCADE_TESTKIT_UPDATE_GOLDEN=1
//
//	and never in CI; otherwise the test compares.
//
// SPORT: cmd/cascade daemon registrations (tests, P1-WID-08).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// assertOnlyWireCallsLoop requires wire_status_widget.go to be the only
// non-test file under cmd/cascade or internal/ that CALLS the loop entry
// point (a comment naming it does not count).
func assertOnlyWireCallsLoop(t *testing.T) {
	t.Helper()
	var hits []string
	for _, pattern := range []string{"*.go", "../../internal/*/*.go", "../../internal/*/*/*.go"} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && strings.HasSuffix(exprText(call.Fun), "RunStatusWidgetRefresh") {
					hits = append(hits, filepath.Base(f))
				}
				return true
			})
		}
	}
	if len(hits) != 1 || hits[0] != "wire_status_widget.go" {
		t.Errorf("RunStatusWidgetRefresh is called from %v, want only wire_status_widget.go", hits)
	}
}

// seedWorld adds the fixture's node, one attention item and two active jobs.
func (ww *widgetWire) seedWorld() {
	ww.t.Helper()
	ctx := context.Background()
	rec := nodes.DeviceRecord{NodeID: "node-0001", Tier: nodes.TierWorkerTrusted, EnrolledAt: widgetWireNow, Presence: nodes.PresenceReachable}
	if err := nodes.NewFileRecordBackend(ww.dataDir).Save(map[string]nodes.DeviceRecord{rec.NodeID: rec}); err != nil {
		ww.t.Fatalf("seed node: %v", err)
	}
	att := supervision.NewStore(ww.w.Store, ww.clock, nil, supervision.NewSystemIDGenerator(), 0)
	if _, err := att.Push(ctx, supervision.AttentionItem{Kind: supervision.KindStall, SourceRef: "session-1", ScopeRef: supervision.ScopeRef{Kind: scope.ScopeKindGlobal}}); err != nil {
		ww.t.Fatalf("seed attention: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(ww.dataDir, "cascade.db"))
	if err != nil {
		ww.t.Fatalf("open cascade.db: %v", err)
	}
	ww.t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, ww.clock, "", ""); err != nil {
		ww.t.Fatalf("jobs schema: %v", err)
	}
	store := jobs.NewStore(db)
	for id, st := range map[string]jobs.JobState{"job-1": jobs.JobStateLeased, "job-2": jobs.JobStateRunning, "job-3": jobs.JobStatePending} {
		job := jobs.Job{ID: id, State: st, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal}
		if err := store.PutJob(ctx, job); err != nil {
			ww.t.Fatalf("seed job: %v", err)
		}
	}
}

// TestStatusWidgetFixtureFromEvidencePath renders status.widget over the
// evidence providers plus one node, one attention item and two active jobs
// and holds internal/daemon's RPC fixture to it (rewrites it only under
// CASCADE_TESTKIT_UPDATE_GOLDEN=1, never in CI).
func TestStatusWidgetFixtureFromEvidencePath(t *testing.T) {
	ww := newWidgetWire(t)
	ww.seedEvidence()
	ww.seedWorld()
	snap := ww.snapshot()
	if snap.AttentionCount != 1 || snap.ActiveJobsCount == nil || *snap.ActiveJobsCount != 2 || len(snap.Nodes) != 1 {
		t.Fatalf("attention %d jobs %v nodes %d, want 1, 2 and 1", snap.AttentionCount, snap.ActiveJobsCount, len(snap.Nodes))
	}
	raw, err := json.Marshal(map[string]any{
		"request": map[string]any{"jsonrpc": "2.0", "method": daemon.MethodStatusWidget, "id": 1}, "result": snap})
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "internal", "daemon", "testdata", "fixture_status_widget_rpc.json")
	got = append(got, '\n')
	if testkit.UpdateRequested() && os.Getenv("CI") == "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatalf("fixture %s differs from the evidence path (read err %v); regenerate with CASCADE_TESTKIT_UPDATE_GOLDEN=1\n--- got ---\n%s", path, err, got)
	}
}
