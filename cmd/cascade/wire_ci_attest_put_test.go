//go:build !windows

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// putRig is a publisher over a fixture db with one accepted row, a recording
// call and a controller key whose signatures a node can verify.
type putRig struct {
	f       *attestFixture
	db      *sql.DB
	pub     *attestPutPublisher
	effects *audit.EffectLog
	ctrlPub ed25519.PublicKey
	calls   map[string][]nodes.AttestPutRequest
	repos   map[string][]string
	nonce   string
	rowID   string
}

func newPutRig(t *testing.T, repoID string, repos map[string][]string) *putRig {
	t.Helper()
	f := newAttestFixture(t)
	db := f.openDB(t)
	t.Cleanup(func() { _ = db.Close() })
	nonce := f.accept(t, db, repoID, "ckpt-1")
	row := f.store(t, db, repoID, nonce, "ckpt-1", 0)
	ctrlPub, ctrl, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewFrozenClock(f.start.Add(2 * time.Minute))
	effects, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), clock, nil))
	if err != nil {
		t.Fatal(err)
	}
	r := &putRig{f: f, db: db, effects: effects, ctrlPub: ctrlPub, calls: map[string][]nodes.AttestPutRequest{}, repos: repos, nonce: nonce, rowID: row.ID}
	rows, _ := jobs.NewCiAttestationStore(db)
	nonces, _ := jobs.NewAttestationNonceStore(db, clock)
	trust, err := attestTrustStore(f.dataDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	r.pub = &attestPutPublisher{rows: rows, nonces: nonces, trust: trust, effects: effects, pending: effects.PendingEffects,
		records: nodes.NewRecordStore(nodes.NewFileRecordBackend(f.dataDir), clock),
		repos:   func() (map[string][]string, error) { return r.repos, nil },
		sign:    func(_ context.Context, msg []byte) ([]byte, error) { return ed25519.Sign(ctrl, msg), nil },
		dial:    r.recordingDial}
	return r
}

func (r *putRig) recordingDial(node string) nodeCall {
	return func(_ context.Context, _ string, params, out any) error {
		r.calls[node] = append(r.calls[node], params.(nodes.AttestPutRequest))
		*(out.(*nodes.AttestPutResult)) = nodes.AttestPutResult{Stored: true}
		return nil
	}
}

// enroll records a second generated node and returns its id.
func (r *putRig) enroll(t *testing.T) string {
	t.Helper()
	id, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(r.f.dataDir), testkit.NewFrozenClock(r.f.start))
	if _, err := records.Enroll(id, nodes.TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	return id.NodeID
}

func TestAttestPutTargetsConfiguredNodesOnly(t *testing.T) {
	r := newPutRig(t, "r1", nil)
	nA, nB := r.f.node.NodeID, r.enroll(t)
	r.repos = map[string][]string{nA: {"r1"}, nB: {"r2"}, "nC-unrecorded": {"r1"}}
	if err := r.pub.publish(context.Background(), r.nonce); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(r.calls) != 1 || len(r.calls[nA]) != 1 {
		t.Fatalf("calls = %v; want exactly one call, to %s", r.calls, nA)
	}
	r2 := newPutRig(t, "r1", map[string][]string{})
	if err := r2.pub.publish(context.Background(), r2.nonce); err != nil || len(r2.calls) != 0 {
		t.Fatalf("empty repos: calls %v, err %v; want none", r2.calls, err)
	}
}

func TestAttestPutPublishedOnAccepted(t *testing.T) {
	r := newPutRig(t, "r1", nil)
	r.repos = map[string][]string{r.f.node.NodeID: {"r1"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv := storetest.NewMemStore()
	bus := events.New(kv, testkit.NewFrozenClock(r.f.start))
	sub, err := bus.Subscribe(ctx, "jobs.accepted", "test-attest-put", 8)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.pub.run(ctx, sub) }()
	for _, n := range []string{"", r.nonce} {
		payload, _ := json.Marshal(map[string]string{"job_id": "job-1", "attestation_nonce": n})
		if _, err := bus.Publish(ctx, "jobs.accepted", jobs.EventJobAccepted, "test", payload); err != nil {
			t.Fatal(err)
		}
	}
	waitForCalls(t, r, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	reqs := r.calls[r.f.node.NodeID]
	if len(reqs) != 1 {
		t.Fatalf("calls = %v; want one (the empty nonce calls nothing)", r.calls)
	}
	checkPutRequest(t, r, reqs[0])
}

func waitForCalls(t *testing.T, r *putRig, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if effectsConfirmed(t, r) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no confirmed put within the wait; calls %v", r.calls)
}

func effectsConfirmed(t *testing.T, r *putRig) int {
	t.Helper()
	n := 0
	for node := range r.calls {
		st, found, err := r.effects.EffectState(context.Background(), attestPutKeyPrefix+node+":"+r.rowID)
		if err != nil {
			t.Fatal(err)
		}
		if found && st == audit.EffectConfirmed {
			n++
		}
	}
	return n
}

func checkPutRequest(t *testing.T, r *putRig, req nodes.AttestPutRequest) {
	t.Helper()
	a := req.Acceptance
	if a.RepoID != "r1" || a.Nonce != r.nonce || a.CommitSHA != r.f.commit || a.TreeHash != r.f.tree ||
		a.CheckpointID != "ckpt-1" || a.JobSpecHash != "spec-1" || a.PolicyVersion != "policy-v1" || !slices.Equal(a.PlanKinds, []string{"lint", "unit"}) {
		t.Fatalf("acceptance = %+v", a)
	}
	payload, err := nodes.AttestPutPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(req.ControllerSig)
	if err != nil || !ed25519.Verify(r.ctrlPub, payload, sig) {
		t.Fatalf("controller_sig does not verify over AttestPutPayload: %v", err)
	}
}

// nodeSide is a node registry whose putter stores into its own db.
func nodeSide(t *testing.T, r *putRig) (*nodes.ServeRegistry, *sql.DB) {
	t.Helper()
	clock := testkit.NewFrozenClock(r.f.start.Add(2 * time.Minute))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	putter, err := ci.NewNodeAttestPutter(db, clock)
	if err != nil {
		t.Fatal(err)
	}
	binding := nodes.NewFileControllerBindingBackend(t.TempDir())
	if err := binding.Save(nodes.ControllerBinding{Endpoint: "http://controller.invalid", EnrollmentID: "enr",
		ControllerPubKeyB64: base64.StdEncoding.EncodeToString(r.ctrlPub)}); err != nil {
		t.Fatal(err)
	}
	return nodes.NewServeRegistry(nodes.ServeDeps{AttestPutter: putter.Put, ControllerBinding: binding}), db
}

func TestAttestPutEffectCrash(t *testing.T) {
	ctx := context.Background()
	r := newPutRig(t, "r1", nil)
	node := r.f.node.NodeID
	r.repos = map[string][]string{node: {"r1"}}
	reg, nodeDB := nodeSide(t, r)
	r.pub.dial = func(string) nodeCall { return reg.Call }
	log := r.effects
	r.pub.effects = noConfirm{log}
	if err := r.pub.publish(ctx, r.nonce); err == nil {
		t.Fatal("publish with a dead confirmation = nil error")
	}
	pending, err := log.PendingEffects(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after the crash = %+v, %v; want one intent", pending, err)
	}
	r.pub.effects = log
	for i := 0; i < 2; i++ {
		if err := r.pub.recover(ctx, pending); err != nil {
			t.Fatalf("recover %d: %v", i, err)
		}
		pending, _ = log.PendingEffects(ctx)
	}
	st, found, err := log.EffectState(ctx, attestPutKeyPrefix+node+":"+r.rowID)
	if err != nil || !found || st != audit.EffectConfirmed || len(pending) != 0 {
		t.Fatalf("effect = %q found %v err %v pending %v; want confirmed and none pending", st, found, err, pending)
	}
	var n int
	if err := nodeDB.QueryRow(`SELECT count(*) FROM ci_attestation`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("node rows = %d, %v; want 1", n, err)
	}
}
