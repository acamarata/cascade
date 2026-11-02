package ci

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nodeAttestRig is one enrolled node (identity in a file vault under a fresh
// HOME, a pinned controller key, its own store and checkout) and the
// controller's view of it (the record trust store and the controller key).
type nodeAttestRig struct {
	a        *NodeAttestor
	self     nodes.Identity
	db       *sql.DB
	reg      *nodes.ServeRegistry
	trust    attest.TrustStore
	ctrl     ed25519.PrivateKey
	repo     string
	commit   string
	clock    fakeClock
	request  nodes.AttestSignRequest
	passExec *fakeExecutor
}

func newNodeAttestRig(t *testing.T) nodeAttestRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx, clock := context.Background(), newTestClock()
	db := openFileDB(t, "node.db")
	for _, err := range []error{ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""), jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	self, key, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := nodes.NewNodeKeystore(secrets.Config{Dir: home, ForceFileVault: true, Service: "node-attest-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(ctx, self.NodeID, key); err != nil {
		t.Fatal(err)
	}
	a, err := NewNodeAttestor(self, ks, db, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctrlPub, ctrlPriv, _ := ed25519.GenerateKey(rand.Reader)
	binding := nodes.NewFileControllerBindingBackend(home)
	if err := binding.Save(nodes.ControllerBinding{Endpoint: "http://controller.invalid", EnrollmentID: "enr", ControllerPubKeyB64: base64.StdEncoding.EncodeToString(ctrlPub)}); err != nil {
		t.Fatal(err)
	}
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(t.TempDir()), clock)
	if _, err := records.Enroll(self, nodes.TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	repo, commit := newGitRepo(t, "main.go", "package main\n")
	return nodeAttestRig{a: a, self: self, db: db, trust: attest.NewRecordTrustStore(records), ctrl: ctrlPriv,
		reg:  nodes.NewServeRegistry(nodes.ServeDeps{Self: self, Clock: clock, AttestSigner: a.Sign, ControllerBinding: binding}),
		repo: repo, commit: commit, clock: clock, passExec: &fakeExecutor{},
		request: nodes.AttestSignRequest{ExecutionID: "exec-1", Nonce: "nonce-1", ExpiresAt: clock.Now().Add(time.Hour),
			CheckpointID: "cp-1", RepoID: "registry-7", JobSpecHash: "spec-1", PolicyVersion: "policy-1"}}
}

func encodeSig(sig []byte) string { return base64.RawStdEncoding.EncodeToString(sig) }

// run executes a real ci.Execute run on the node store through the attestor.
func (r nodeAttestRig) run(t *testing.T, executionID string, runID int64, exec Executor) {
	t.Helper()
	cfg := RunnerConfig{RepoRoot: r.repo, Steps: []RunnerStep{{Kind: StepLint, Command: "lint"}, {Kind: StepTest, Command: "test"}}}
	if _, err := r.a.Execute(context.Background(), Deps{Clock: r.clock, Exec: exec}, cfg, executionID, runID, 7, "repo"); err != nil {
		t.Fatal(err)
	}
}

// request signs req as the pinned controller and calls the node registry.
func (r nodeAttestRig) ask(req nodes.AttestSignRequest) (attest.SignedAttestation, error) {
	sign := func(p []byte) ([]byte, error) { return ed25519.Sign(r.ctrl, p), nil }
	return RequestNodeSignature(context.Background(), r.reg.Call, sign, r.self.NodeID, req)
}

func wantCIKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if kind, ok := cascade.KindOf(err); !ok || kind != want {
		t.Fatalf("got %v (kind %v ok %v), want %v", err, kind, ok, want)
	}
}

func TestNodeSignsAttestationAfterCIRun(t *testing.T) {
	r := newNodeAttestRig(t)
	r.run(t, "exec-1", -11, r.passExec)
	sa, err := r.ask(r.request)
	if err != nil {
		t.Fatal(err)
	}
	tree := treeOf(t, r.repo, r.commit)
	rec := sa.Record
	if rec.ExitStatus != 0 || rec.CommitSHA != r.commit || rec.TreeHash != tree || len(rec.ResultHashes) != 2 ||
		rec.NodeID != r.self.NodeID || rec.KeyID != nodes.KeyIDFor(r.self.PubKey) || rec.AttestorClass != attest.ClassEnrolledNode {
		t.Fatalf("record not built from the run: %+v", rec)
	}
	if err := attest.VerifySignature(context.Background(), sa, r.commit, tree, r.trust, r.clock.Now()); err != nil {
		t.Fatalf("controller rejects the node's record: %v", err)
	}
	t.Run("failing run signs a failing record", func(t *testing.T) {
		r.run(t, "exec-2", -12, &fakeExecutor{results: map[string]ExecResult{"test": {ExitCode: 3}}})
		req := r.request
		req.ExecutionID, req.Nonce = "exec-2", "nonce-2"
		sa, err := r.ask(req)
		if err != nil || sa.Record.ExitStatus != 1 {
			t.Fatalf("failing run: exit %d err %v", sa.Record.ExitStatus, err)
		}
	})
	t.Run("a record signed by another key is refused", func(t *testing.T) {
		forged := sa
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		forged.Signature = encodeSig(ed25519.Sign(other, append([]byte("cascade.ci.attestation.v1\x1f"), sa.CanonicalJSON...)))
		if err := attest.VerifySignature(context.Background(), forged, r.commit, tree, r.trust, r.clock.Now()); err == nil {
			t.Fatal("forged signature accepted")
		}
	})
}

func TestNodeSignRefusesForeignExecution(t *testing.T) {
	r := newNodeAttestRig(t)
	_, err := r.ask(r.request)
	wantCIKind(t, err, cascade.KindPermissionDenied)
	t.Run("incomplete run", func(t *testing.T) {
		r.run(t, "exec-1", -21, r.passExec)
		if _, err := r.db.Exec(`UPDATE ci_run SET status='in_progress' WHERE run_id=-21`); err != nil {
			t.Fatal(err)
		}
		_, err := r.ask(r.request)
		wantCIKind(t, err, cascade.KindPermissionDenied)
	})
	t.Run("a run the poller wrote", func(t *testing.T) {
		run := sampleRun(-22)
		if err := Upsert(context.Background(), r.db, run, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := r.a.runs.RecordRun(context.Background(), jobs.NodeRun{ExecutionID: "exec-gh", RunID: -22, RepoID: 7, CommitSHA: r.commit, Checkout: r.repo}); err != nil {
			t.Fatal(err)
		}
		req := r.request
		req.ExecutionID = "exec-gh"
		_, err := r.ask(req)
		wantCIKind(t, err, cascade.KindPermissionDenied)
	})
}

func TestNodeSignRefusesSecondSign(t *testing.T) {
	r := newNodeAttestRig(t)
	r.run(t, "exec-1", -31, r.passExec)
	if _, err := r.ask(r.request); err != nil {
		t.Fatal(err)
	}
	again := r.request
	again.Nonce = "nonce-other"
	_, err := r.ask(again)
	wantCIKind(t, err, cascade.KindConflict)
	if _, err := r.a.Sign(context.Background(), r.request); err == nil {
		t.Fatal("direct second sign allowed")
	}
}

func TestNodeSignCopiesFactFields(t *testing.T) {
	r := newNodeAttestRig(t)
	r.run(t, "exec-1", -41, r.passExec)
	alterations := map[string]func(*nodes.AttestSignRequest){
		"repo_id":        func(q *nodes.AttestSignRequest) { q.RepoID = "registry-8" },
		"job_spec_hash":  func(q *nodes.AttestSignRequest) { q.JobSpecHash = "spec-2" },
		"policy_version": func(q *nodes.AttestSignRequest) { q.PolicyVersion = "policy-2" },
	}
	for name, alter := range alterations {
		tamper := func(ctx context.Context, method string, params, out any) error {
			q := params.(nodes.AttestSignRequest)
			alter(&q)
			return r.reg.Call(ctx, method, q, out)
		}
		sign := func(p []byte) ([]byte, error) { return ed25519.Sign(r.ctrl, p), nil }
		_, err := RequestNodeSignature(context.Background(), tamper, sign, r.self.NodeID, r.request)
		if kind, _ := cascade.KindOf(err); kind != cascade.KindPermissionDenied {
			t.Fatalf("%s altered after signing: %v", name, err)
		}
	}
	sa, err := r.ask(r.request)
	if err != nil {
		t.Fatal(err)
	}
	rec := sa.Record
	if rec.RepoID != r.request.RepoID || rec.JobSpecHash != r.request.JobSpecHash || rec.PolicyVersion != r.request.PolicyVersion ||
		rec.CheckpointID != r.request.CheckpointID || rec.Nonce != r.request.Nonce || !strings.Contains(string(sa.CanonicalJSON), `"repo_id":"registry-7"`) {
		t.Fatalf("record did not copy the signed facts: %+v", rec)
	}
}
