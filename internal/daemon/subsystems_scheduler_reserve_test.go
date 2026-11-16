package daemon

// Purpose: RegisterScheduler's reservation seam. Registration refuses
//
//	when SetSchedulerReservation bound nothing, and at start every job
//	Resume re-entered at `leased` is re-admitted through the seam: a live
//	delegation row's reservation is adopted, otherwise a fresh one is
//	reserved, and a reserve failure leaves the job waiting.
//
// SPORT: internal/daemon (EXTEND).

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// seamRecorder is a fake fleet reservation seam that records each call.
type seamRecorder struct {
	adopted  []string
	reserved []string
	failJob  string
}

func (r *seamRecorder) adopt(_ context.Context, reservationID string) error {
	r.adopted = append(r.adopted, reservationID)
	return nil
}

func (r *seamRecorder) reserve(_ context.Context, jobID string) error {
	r.reserved = append(r.reserved, jobID)
	if jobID == r.failJob {
		return errors.New("fleet saturated")
	}
	return nil
}

// bindTestReservation binds a seam that always succeeds, for tests whose
// subject is not the seam itself.
func bindTestReservation(m *Manifest) *seamRecorder {
	rec := &seamRecorder{}
	m.SetSchedulerReservation(rec.reserve, rec.adopt)
	return rec
}

func TestRegisterSchedulerRequiresReserver(t *testing.T) {
	rec := &seamRecorder{}
	refusals := map[string]func(m *Manifest){
		"unset":        func(*Manifest) {},
		"nil reserve":  func(m *Manifest) { m.SetSchedulerReservation(nil, rec.adopt) },
		"nil adopt":    func(m *Manifest) { m.SetSchedulerReservation(rec.reserve, nil) },
		"both nil set": func(m *Manifest) { m.SetSchedulerReservation(nil, nil) },
	}
	for name, bind := range refusals {
		t.Run(name, func(t *testing.T) {
			m := NewManifest(nil, runtime.NewSystemClock())
			bind(m)
			sched, err := m.RegisterScheduler(context.Background(), newSchedulerTestBus(), runtime.NewSystemClock(), designatedControllerBinding(t), "sched-test-noseam", nil, nil, 0)
			if err != errSchedulerReservationUnset || sched != nil {
				t.Fatalf("RegisterScheduler without a seam = %v, %v; want nil, errSchedulerReservationUnset", sched, err)
			}
			if !strings.Contains(err.Error(), "no reservation seam bound") {
				t.Fatalf("refusal message = %q, want it to name the missing seam", err.Error())
			}
			assertSchedulerErrorDetail(t, m, "no reservation seam bound")
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManifest(nil, runtime.NewSystemClock())
	bindTestReservation(m)
	sched, err := m.RegisterScheduler(ctx, newSchedulerTestBus(), runtime.NewSystemClock(), designatedControllerBinding(t), "sched-test-seam", nil, nil, 0)
	if err != nil || sched == nil {
		t.Fatalf("RegisterScheduler with a seam = %v, %v; want a scheduler and no error", sched, err)
	}
}

func TestResumeReadmitsReenteredJobs(t *testing.T) {
	store := newReadmitTestStore(t)
	js := journal.New(storetest.NewMemStore(), runtime.NewFixedClock(fixedResumeNow), journal.DefaultNamespace)
	for i, id := range []string{"job-adopt", "job-reserve", "job-fail"} {
		seedStaleJobAtScope(t, store, js, id, []string{"a/**", "b/**", "c/**"}[i])
	}
	insertLiveDelegation(t, store, "job-adopt", "res-held")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManifest(nil, runtime.NewFixedClock(fixedResumeNow))
	rec := &seamRecorder{failJob: "job-fail"}
	m.SetSchedulerReservation(rec.reserve, rec.adopt)
	if _, err := m.RegisterScheduler(ctx, newSchedulerTestBus(), runtime.NewFixedClock(fixedResumeNow), designatedControllerBinding(t), "sched-test-readmit", store, js, 0); err != nil {
		t.Fatalf("RegisterScheduler: %v", err)
	}

	want := map[string]jobs.JobState{"job-adopt": jobs.JobStateRunning, "job-reserve": jobs.JobStateRunning, "job-fail": jobs.JobStateLeased}
	for id, st := range want {
		got, ok, err := store.GetJob(context.Background(), id)
		if err != nil || !ok || got.State != st {
			t.Fatalf("job %s after start = %q (ok=%v err=%v), want %q", id, got.State, ok, err, st)
		}
	}
	if strings.Join(rec.adopted, ",") != "res-held" {
		t.Fatalf("adopted = %v, want only the committed reservation res-held", rec.adopted)
	}
	if strings.Join(rec.reserved, ",") != "job-fail,job-reserve" {
		t.Fatalf("reserved = %v, want job-fail,job-reserve (the jobs with no committed row)", rec.reserved)
	}
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem && (s.State != SubsystemRunning || !strings.Contains(s.Detail, "readmitted 2, 1 left waiting")) {
			t.Fatalf("jobs.scheduler = %v %q, want Running with 2 readmitted and 1 waiting", s.State, s.Detail)
		}
	}
}

// assertSchedulerErrorDetail requires the jobs.scheduler entry to be in
// the Error state with a detail naming want.
func assertSchedulerErrorDetail(t *testing.T, m *Manifest, want string) {
	t.Helper()
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem {
			if s.State != SubsystemError || !strings.Contains(s.Detail, want) {
				t.Fatalf("jobs.scheduler = %v %q, want Error naming %q", s.State, s.Detail, want)
			}
			return
		}
	}
	t.Fatalf("Manifest snapshot has no %q entry", jobSchedulerSubsystem)
}

// newReadmitTestStore opens a jobs store with the jobs, outbox and
// delegation schemas applied.
func newReadmitTestStore(t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, clock := context.Background(), runtime.NewFixedClock(fixedResumeNow)
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	if err := jobs.ApplyDelegationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyDelegationSchema: %v", err)
	}
	return jobs.NewStore(db)
}

// seedStaleJobAtScope is seedStaleRunningJob with a caller-chosen scope,
// so several jobs can each hold their own stale lease.
func seedStaleJobAtScope(t *testing.T, store *jobs.Store, js journal.Store, id, scope string) {
	t.Helper()
	ctx := context.Background()
	if err := store.PutJob(ctx, schedulerResumeTestJob(id)); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutLease(ctx, jobs.ResourceLease{
		RepoID: "repo-1", ScopeGlob: scope, Holder: id, IssuedAt: 1,
		TTLSeconds: jobs.DefaultLeaseDefaults().TTLSeconds, Epoch: 1, State: jobs.LeaseHeld,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	if _, err := js.Append(ctx, id, journal.KindIntent, "seed-"+id, []byte(`{}`)); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}
}

// readmitSigner is an in-memory envelope.Signer over a fixed seed.
type readmitSigner struct{ priv ed25519.PrivateKey }

func (s readmitSigner) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

func (s readmitSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, msg), nil
}

// insertLiveDelegation stores a live first-hop delegation for jobID
// holding reservationID: the committed row Readmit adopts.
func insertLiveDelegation(t *testing.T, store *jobs.Store, jobID, reservationID string) {
	t.Helper()
	signer := readmitSigner{priv: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	rootSigned, root := issueReadmitEnvelope(t, signer, "root", "owner-cli", "")
	digest, err := envelope.Digest(root)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	childSigned, child := issueReadmitEnvelope(t, signer, "dlg-"+jobID, "agent-"+jobID, digest)
	err = store.InsertDelegation(context.Background(), jobs.DelegationRecord{
		ID: child.ID, Signed: childSigned, ParentSigned: rootSigned, AgentPrincipal: child.Principal,
		JobID: jobID, ExecutionID: "exec-" + jobID, ReservationID: reservationID,
		State: jobs.DelegationLive, CreatedAt: fixedResumeNow,
	})
	if err != nil {
		t.Fatalf("InsertDelegation: %v", err)
	}
}

func issueReadmitEnvelope(t *testing.T, signer readmitSigner, id, principal, parentDigest string) ([]byte, envelope.Envelope) {
	t.Helper()
	issued := fixedResumeNow.UTC()
	signed, err := envelope.Issue(context.Background(), signer, envelope.Envelope{
		Version: envelope.Version, ID: id, ParentDigest: parentDigest, Principal: principal,
		Roots: []string{"/work"}, Tools: []string{"agent.tools.all"}, Commands: []string{},
		Egress: []string{"agent-driver"}, Grants: []envelope.GrantRef{},
		Budget:      envelope.Budget{WallClock: time.Hour, Tokens: 100, Procs: 2},
		Sensitivity: provider.SensitivityInternal, IssuedAt: issued, ExpiresAt: issued.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Issue(%s): %v", id, err)
	}
	b, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b, signed.Env
}
