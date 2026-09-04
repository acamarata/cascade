//go:build !windows

// Purpose: the production-path proof for the two memory maintenance jobs.
//
//	It does NOT build a scheduler of its own and hand-register runnables:
//	it calls startScheduler — the exact function platformDaemonRun calls —
//	and then FIRES the registered jobs through the scheduler's own Tick.
//	That distinction is the whole point of this file. The retention prune
//	job shipped for a whole phase returning an error on every fire because
//	production registered it with a zero config and every test built its
//	own registration with the field filled in
//	(internal/events/scheduler/retention_clock_test.go). A test that
//	constructs its own runnable cannot see that class of defect; only one
//	that fires what the composition root actually built can.
//
// SPORT: cmd/cascade/daemon (CHANGED — P1-E07-W2-S13-T4 memory job wiring
//
//	verification).
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
)

// memoryJobEpoch is the instant the frozen clock starts at.
var memoryJobEpoch = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

// TestStartScheduler_MemoryJobsFireThroughTheProductionRegistration is the
// regression test for the retention-class defect.
//
// It seeds two byte-identical records, starts the real scheduler through
// startScheduler, advances the clock past the jobs' daily schedule, and
// Ticks. Both jobs must fire and BOTH must succeed: a job that errors on
// every fire — because production hands it a collaborator no test
// happened to supply — is a daemon that looks alive and maintains nothing.
//
// It then asserts the consolidation actually happened on disk, so a
// runnable that returns nil without doing anything cannot pass either.
func TestStartScheduler_MemoryJobsFireThroughTheProductionRegistration(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(memoryJobEpoch)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	base := memoryStoreDir(paths)
	seedDuplicateRecords(t, base, clock)

	store, rawDB, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	t.Cleanup(closeStore)
	bus := events.New(store, clock)
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	// A one-minute schedule, supplied through the SAME [memory] config
	// path production reads, so this test also proves ParseJobConfig is
	// wired: the default is daily, and advancing a day would outlive the
	// scheduler's five-minute advisory lease.
	cfg := &runtime.Config{Extra: map[string]any{"memory": map[string]any{
		"consolidation_schedule": "@every 1m",
		"staleness_schedule":     "@every 1m",
	}}}
	sched, _, cleanup, err := startScheduler(ctx, store, rawDB, paths, cfg, clock, bus, logger)
	if err != nil {
		t.Fatalf("startScheduler: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		cleanup(context.Background())
	})

	clock.Advance(90 * time.Second)
	report, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	assertFiredCleanly(t, report.Fired, report.Errors)

	// The consolidation must have actually happened, not merely returned
	// nil: the older record survives and the newer one is tombstoned.
	assertRecordGone(t, base, "project", "dup-newer")
	assertRecordLive(t, base, "project", "dup-older")
	assertConsolidationRecordNames(t, base, "project/dup-older", "project/dup-newer")
}

// assertFiredCleanly checks that both memory jobs fired and that no job
// returned an error.
func assertFiredCleanly(t *testing.T, fired []string, errs []error) {
	t.Helper()
	for _, e := range errs {
		t.Errorf("a scheduled job returned an error on its first real fire: %v", e)
	}
	want := map[string]bool{memoryConsolidateOwner: false, memoryStalenessOwner: false}
	for _, id := range fired {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("job %q did not fire; Tick fired %v", id, fired)
		}
	}
}

// seedDuplicateRecords writes two records with byte-identical bodies (and
// deliberately DIFFERENT descriptions, so the test also proves the
// retired record's own description is preserved in the account) plus one
// record that is not a duplicate.
func seedDuplicateRecords(t *testing.T, base string, clock *testkit.FrozenClock) {
	t.Helper()
	store := memory.NewFileStore(base, clock)
	ctx := context.Background()
	entries := []memory.MemoryEntry{
		{Name: "dup-older", Kind: memory.KindProject, Description: "the first phrasing",
			Body: "the same thing", ScopeRef: "local", Confidence: 1,
			Provenance: memory.Provenance{Origin: memory.OriginSession}},
		{Name: "dup-newer", Kind: memory.KindProject, Description: "a later phrasing",
			Body: "the same thing", ScopeRef: "local", Confidence: 1,
			Provenance: memory.Provenance{Origin: memory.OriginSession}},
		{Name: "distinct", Kind: memory.KindProject, Description: "unrelated",
			Body: "something else", ScopeRef: "local", Confidence: 1,
			Provenance: memory.Provenance{Origin: memory.OriginSession}},
	}
	for i, e := range entries {
		// Distinct CreatedAt instants make the survivor deterministic:
		// the oldest record is the one a user remembers writing.
		clock.Advance(time.Duration(i) * time.Minute)
		if err := store.Write(ctx, e); err != nil {
			t.Fatalf("seeding %s: %v", e.Name, err)
		}
	}
}

// assertRecordGone fails unless the named record is tombstoned.
func assertRecordGone(t *testing.T, base, kind, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(base, kind, name+".md.tombstone")); err != nil {
		t.Errorf("no tombstone for %s/%s: the job fired but retired nothing (%v)", kind, name, err)
	}
}

// assertRecordLive fails unless the named record's file is still present.
func assertRecordLive(t *testing.T, base, kind, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(base, kind, name+".md")); err != nil {
		t.Errorf("the surviving record %s/%s is gone: %v", kind, name, err)
	}
}

// assertConsolidationRecordNames fails unless the consolidation record for
// survivor names every one of wantMembers.
//
// This is the "never lose a memory silently" assertion. A merge that
// retires a record without leaving an account of it is exactly the failure
// this subsystem must not have, so the account is checked on disk rather
// than inferred from the report.
func assertConsolidationRecordNames(t *testing.T, base, survivor string, wantMembers ...string) {
	t.Helper()
	kind, name, ok := strings.Cut(survivor, "/")
	if !ok {
		t.Fatalf("bad survivor address %q", survivor)
	}
	path := filepath.Join(base, "consolidations", kind, name+".consolidation.json")
	data, err := os.ReadFile(path) //nolint:gosec // a path this test itself built
	if err != nil {
		t.Fatalf("no consolidation record for %s: %v", survivor, err)
	}
	var rec memory.ConsolidationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("consolidation record for %s does not parse: %v", survivor, err)
	}
	have := map[string]memory.RetiredMember{}
	for _, m := range rec.Members {
		have[m.ID] = m
	}
	for _, want := range wantMembers {
		member, found := have[want]
		if !found {
			t.Errorf("consolidation record for %s does not account for retired record %s", survivor, want)
			continue
		}
		if member.Description == "" {
			t.Errorf("the account of retired record %s kept no description", want)
		}
	}
	if rec.Body == "" {
		t.Errorf("the consolidation record for %s kept no body, so the retired records are not reconstructible", survivor)
	}
}
