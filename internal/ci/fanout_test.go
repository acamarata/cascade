package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fanoutStart is the frozen instant fanout fixtures are written at.
var fanoutStart = time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)

// fakeOrigins answers OriginForJob from a fixed record.
type fakeOrigins struct {
	rec   jobs.DelegationRecord
	scope string
	found bool
}

func (f fakeOrigins) OriginForJob(context.Context, string) (jobs.DelegationRecord, string, bool, error) {
	return f.rec, f.scope, f.found, nil
}

// recNotifier records every delivered notification, or fails with err.
type recNotifier struct {
	mu  sync.Mutex
	got []notify.Notification
	err error
}

func (r *recNotifier) Deliver(_ context.Context, n notify.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.got = append(r.got, n)
	return nil
}

// captureWarn swaps Warn for a recorder for the length of the test.
func captureWarn(t *testing.T) *[]string {
	t.Helper()
	var msgs []string
	prev := Warn
	Warn = func(msg string, _ ...any) { msgs = append(msgs, msg) }
	t.Cleanup(func() { Warn = prev })
	return &msgs
}

// fanoutDB opens a migrated ci database holding run runID of repo 7 with
// the given conclusion and its one job.
func fanoutDB(t *testing.T, runID int64, conclusion RunConclusion) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	writeFanoutRun(t, db, runID, conclusion)
	return db
}

func writeFanoutRun(t *testing.T, db *sql.DB, runID int64, conclusion RunConclusion) {
	t.Helper()
	run := Run{RunID: runID, RepoID: 7, Name: "stream", HeadSHA: fmt.Sprintf("sha-%d", runID),
		Status: RunStatusCompleted, Conclusion: conclusion, CreatedAt: fanoutStart, UpdatedAt: fanoutStart.Add(time.Minute)}
	job := Job{JobID: runID, RunID: runID, Name: "test", Status: RunStatusCompleted, Conclusion: conclusion,
		StartedAt: fanoutStart.Add(time.Second), FinishedAt: fanoutStart.Add(30 * time.Second)}
	if err := Upsert(context.Background(), db, run, []Job{job}, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func fanoutEvent(runID int64) CIResultEvent {
	return CIResultEvent{JobID: "job-1", RunID: runID, RepoID: 7, Kind: "test", Passed: true, ExecutorKind: "local"}
}

func delegated(principalID string) fakeOrigins {
	return fakeOrigins{found: true, scope: "project:p1",
		rec: jobs.DelegationRecord{ID: "deleg-1", OriginPrincipal: principalID, OriginSession: "sess-a", JobID: "job-1"}}
}

func newFanoutService() *notify.Service {
	return notify.NewService(notify.DefaultConfig(), testkit.NewFrozenClock(fanoutStart),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func asCaller(id string) context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{
		Principal: &principal.Principal{ID: id, Kind: principal.KindSession, SessionID: "sess-" + id}})
}

// fireFanout registers the fanout on a bare Dispatcher and fires ev once.
func fireFanout(db *sql.DB, origins DelegationOrigin, n Notifier, pusher AttentionPusher, ev CIResultEvent) {
	d := &Dispatcher{}
	RegisterCIFanout(d, db, origins, n, pusher)
	d.notifyTerminal(ev)
}

func TestCIFanoutAddressedToOriginPrincipal(t *testing.T) {
	db := fanoutDB(t, -11, ConclusionSuccess)
	svc := newFanoutService()
	pusher := &fakePusher{}
	d := &Dispatcher{}
	RegisterCIFanout(d, db, delegated("prin-a"), svc.Notifier, pusher)
	if len(d.terminals) != 1 {
		t.Fatalf("RegisterCIFanout registered %d callbacks, want 1", len(d.terminals))
	}
	d.notifyTerminal(fanoutEvent(-11))
	svc.Dispatcher.Drain(context.Background())
	got, err := svc.ReadAddressed(asCaller("prin-a"), "deleg-1")
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadAddressed = %d items, %v; want one", len(got), err)
	}
	n := got[0]
	if n.Class != notify.ClassAddressed || n.TargetPrincipal != "prin-a" || n.TargetSession != "" ||
		n.Source != CIResultKind || n.Kind != CIResultKind || n.CorrelationID != "deleg-1" {
		t.Fatalf("notification = %+v", n)
	}
	var notice CIResultNotice
	if err := json.Unmarshal(n.Payload, &notice); err != nil {
		t.Fatalf("payload: %v", err)
	}
	want := CIResultNotice{JobID: "job-1", DelegationID: "deleg-1", RepoID: "7", CommitSHA: "sha--11",
		Status: ResultAccepted, LogRef: "ci_run:7:-11", OriginPrincipal: "prin-a", OriginSession: "sess-a",
		OriginScope: "project:p1", StartedAt: fanoutStart.Add(time.Second), FinishedAt: fanoutStart.Add(30 * time.Second)}
	if notice != want {
		t.Fatalf("notice = %+v\nwant %+v", notice, want)
	}
	if len(pusher.pushed) != 0 {
		t.Fatalf("an addressed result also pushed %d attention items", len(pusher.pushed))
	}
}

func TestCIFanoutScopeOnlyScoped(t *testing.T) {
	warns := captureWarn(t)
	db := fanoutDB(t, -12, ConclusionFailure)
	rn, pusher := &recNotifier{}, &fakePusher{}
	origins := delegated("")
	fireFanout(db, origins, rn, pusher, fanoutEvent(-12))
	if len(rn.got) != 1 || len(pusher.pushed) != 0 {
		t.Fatalf("delivered %d, pushed %d; want one scoped delivery", len(rn.got), len(pusher.pushed))
	}
	n := rn.got[0]
	if n.Class != notify.ClassScoped || n.TargetScope != "project:p1" || n.TargetPrincipal != "" ||
		n.TargetSession != "" || n.Kind != CIResultKind {
		t.Fatalf("notification = %+v", n)
	}
	if len(*warns) != 1 {
		t.Fatalf("warnings = %v, want one", *warns)
	}
}

func TestCIFanoutNoOriginFallsBackToAttention(t *testing.T) {
	warns := captureWarn(t)
	db := fanoutDB(t, -13, ConclusionFailure)
	rn, pusher := &recNotifier{}, &fakePusher{}
	fireFanout(db, fakeOrigins{}, rn, pusher, fanoutEvent(-13))
	if len(rn.got) != 0 || len(pusher.pushed) != 1 {
		t.Fatalf("delivered %d, pushed %d; want one attention item", len(rn.got), len(pusher.pushed))
	}
	if pusher.pushed[0].SourceRef != "ci.result:job-1:ci_run:7:-13" {
		t.Fatalf("attention item = %+v", pusher.pushed[0])
	}
	if len(*warns) != 1 {
		t.Fatalf("warnings = %v, want one", *warns)
	}
}

func TestCIFanoutDeliverError(t *testing.T) {
	captureWarn(t)
	boom := errors.New("boom")
	addressed := CIResultNotice{JobID: "job-1", DelegationID: "deleg-1", OriginPrincipal: "prin-a"}
	if err := RouteToInbox(context.Background(), addressed, &recNotifier{err: boom}, &fakePusher{}); !errors.Is(err, boom) {
		t.Fatalf("addressed Deliver failure = %v, want boom", err)
	}
	scoped := CIResultNotice{JobID: "job-1", OriginScope: "project:p1"}
	if err := RouteToInbox(context.Background(), scoped, &recNotifier{err: boom}, &fakePusher{}); !errors.Is(err, boom) {
		t.Fatalf("scoped Deliver failure = %v, want boom", err)
	}
	none := CIResultNotice{JobID: "job-1"}
	if err := RouteToInbox(context.Background(), none, &recNotifier{}, &fakePusher{failWith: boom}); !errors.Is(err, boom) {
		t.Fatalf("Push failure = %v, want boom", err)
	}
}

func TestCIFanoutStatusTable(t *testing.T) {
	cases := []struct {
		conclusion RunConclusion
		want       string
	}{
		{ConclusionSuccess, ResultAccepted},
		{ConclusionFailure, ResultRejected},
		{ConclusionCancelled, ResultCancelled},
		{ConclusionTimedOut, ResultFailed},
		{ConclusionSkipped, ResultFailed},
		{ConclusionUnknown, ResultFailed},
		{ConclusionNone, ResultFailed},
	}
	for i, tc := range cases {
		db := fanoutDB(t, int64(-100-i), tc.conclusion)
		notice, err := ProjectResult(context.Background(), db, fakeOrigins{}, fanoutEvent(int64(-100-i)))
		if err != nil || notice.Status != tc.want {
			t.Errorf("conclusion %q: status %q, %v; want %q", tc.conclusion, notice.Status, err, tc.want)
		}
		_ = db.Close()
	}
}

func TestCIFanoutReadsRunByEventKey(t *testing.T) {
	warns := captureWarn(t)
	db := fanoutDB(t, -21, ConclusionSuccess)
	writeFanoutRun(t, db, -22, ConclusionFailure)
	notice, err := ProjectResult(context.Background(), db, fakeOrigins{}, fanoutEvent(-22))
	if err != nil || notice.CommitSHA != "sha--22" || notice.Status != ResultRejected {
		t.Fatalf("ProjectResult(-22) = %+v, %v", notice, err)
	}
	_, err = ProjectResult(context.Background(), db, fakeOrigins{}, fanoutEvent(-99))
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("missing run = %v, want KindNotFound", err)
	}
	rn, pusher := &recNotifier{}, &fakePusher{}
	fireFanout(db, delegated("prin-a"), rn, pusher, fanoutEvent(-99))
	if len(rn.got) != 0 || len(pusher.pushed) != 0 || len(*warns) != 1 {
		t.Fatalf("missing run delivered %d, pushed %d, warned %v; want only a warning",
			len(rn.got), len(pusher.pushed), *warns)
	}
}

func TestCIFanoutOtherPrincipalCannotRead(t *testing.T) {
	db := fanoutDB(t, -31, ConclusionSuccess)
	svc := newFanoutService()
	fireFanout(db, delegated("prin-a"), svc.Notifier, &fakePusher{}, fanoutEvent(-31))
	svc.Dispatcher.Drain(context.Background())
	got, err := svc.ReadAddressed(asCaller("prin-b"), "deleg-1")
	if err != nil || len(got) != 0 {
		t.Fatalf("other principal read %d items, %v; want none", len(got), err)
	}
	id := "ci.result:job-1:ci_run:7:-31"
	if _, err := svc.ReadAddressedByID(asCaller("prin-b"), id); !isKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("other principal ReadAddressedByID = %v, want KindPermissionDenied", err)
	}
	if _, err := svc.ReadAddressedByID(asCaller("prin-a"), id); err != nil {
		t.Fatalf("addressee ReadAddressedByID: %v", err)
	}
}
