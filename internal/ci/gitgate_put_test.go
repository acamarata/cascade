package ci

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

type putRig struct {
	nodeAttestRig
	reg    *nodes.ServeRegistry
	q      nodes.AttestPutRequest
	sa     attest.SignedAttestation
	rows   *jobs.CiAttestationStore
	nonces *jobs.AttestationNonceStore
	trust  *jobs.CiAttestTrustStore
}

func newPutRig(t *testing.T) putRig {
	t.Helper()
	r := newNodeAttestRig(t)
	r.run(t, "exec-1", -51, r.passExec)
	sa, err := r.ask(r.request)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.a.NewAttestPutter()
	if err != nil {
		t.Fatal(err)
	}
	binding := nodes.NewFileControllerBindingBackend(t.TempDir())
	if err := binding.Save(nodes.ControllerBinding{Endpoint: "http://controller.invalid", EnrollmentID: "enr",
		ControllerPubKeyB64: base64.StdEncoding.EncodeToString(r.ctrl.Public().(ed25519.PublicKey))}); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(sa)
	if err != nil {
		t.Fatal(err)
	}
	rec := sa.Record
	q := nodes.AttestPutRequest{SignedAttestation: wire, SignerPubKeyB64: base64.StdEncoding.EncodeToString(r.self.PubKey),
		Acceptance: nodes.AttestPutAcceptance{CommitSHA: rec.CommitSHA, TreeHash: rec.TreeHash, CheckpointID: rec.CheckpointID,
			Nonce: rec.Nonce, RepoID: rec.RepoID, JobSpecHash: rec.JobSpecHash, PolicyVersion: rec.PolicyVersion,
			PlanKinds: []string{"lint", "test"}, AcceptedAt: r.clock.Now()}}
	rows, _ := jobs.NewCiAttestationStore(r.db)
	nonces, _ := jobs.NewAttestationNonceStore(r.db, r.clock)
	trust, _ := jobs.NewCiAttestTrustStore(r.db)
	return putRig{nodeAttestRig: r, reg: nodes.NewServeRegistry(nodes.ServeDeps{AttestPutter: p.Put, ControllerBinding: binding}),
		q: q, sa: sa, rows: rows, nonces: nonces, trust: trust}
}

func (r putRig) signed(t *testing.T, q nodes.AttestPutRequest) nodes.AttestPutRequest {
	t.Helper()
	payload, err := nodes.AttestPutPayload(q)
	if err != nil {
		t.Fatal(err)
	}
	q.ControllerSig = base64.StdEncoding.EncodeToString(ed25519.Sign(r.ctrl, payload))
	return q
}

func (r putRig) put(q nodes.AttestPutRequest) (nodes.AttestPutResult, error) {
	var out nodes.AttestPutResult
	err := r.reg.Call(context.Background(), nodes.AttestPutMethod, q, &out)
	return out, err
}

// TestAttestPutStoresRowAndAcceptance checks delivery and idempotent storage.
func TestAttestPutStoresRowAndAcceptance(t *testing.T) {
	r := newPutRig(t)
	ctx := context.Background()
	// The redundant wire record must never override the canonical bytes.
	spoof := r.sa
	spoof.Record.RepoID = "untrusted-wire-record"
	wire, err := json.Marshal(spoof)
	if err != nil {
		t.Fatal(err)
	}
	r.q.SignedAttestation = wire
	q := r.signed(t, r.q)
	out, err := r.put(q)
	if err != nil || !out.Stored {
		t.Fatalf("put: %+v %v", out, err)
	}
	row, ok, err := r.rows.ByID(ctx, attest.RowID(r.sa))
	if err != nil || !ok || row.RepoID != r.q.Acceptance.RepoID || row.AttestorClass != string(attest.ClassEnrolledNode) {
		t.Fatalf("row: %+v %v %v", row, ok, err)
	}
	f, ok, err := r.nonces.AcceptedFor(ctx, q.Acceptance.Nonce, r.commit, r.sa.Record.TreeHash, q.Acceptance.CheckpointID)
	if err != nil || !ok || f.RepoID != q.Acceptance.RepoID || f.JobSpecHash != q.Acceptance.JobSpecHash || f.PolicyVersion != q.Acceptance.PolicyVersion {
		t.Fatalf("fact: %+v %v %v", f, ok, err)
	}
	pub, ok, err := r.trust.Lookup(ctx, r.self.NodeID, r.sa.Record.KeyID)
	if err != nil || !ok || pub != q.SignerPubKeyB64 {
		t.Fatalf("trust: %v %v", ok, err)
	}
	if _, err := r.put(q); err != nil {
		t.Fatal(err)
	}
	rows, err := r.rows.ByNonce(ctx, q.Acceptance.Nonce)
	if err != nil || len(rows) != 1 || rows[0] != row {
		t.Fatalf("repeat: %+v %v", rows, err)
	}
}

// TestAttestPutFactCarriesBinding checks rejection before any persistence.
func TestAttestPutFactCarriesBinding(t *testing.T) {
	for _, field := range []string{"repo", "spec", "policy", "consistent", "key", "kinds"} {
		t.Run(field, func(t *testing.T) {
			r := newPutRig(t)
			q := r.signed(t, r.q)
			want := cascade.KindPermissionDenied
			switch field {
			case "repo":
				q.Acceptance.RepoID = "other"
			case "spec":
				q.Acceptance.JobSpecHash = "other"
			case "policy":
				q.Acceptance.PolicyVersion = "other"
			case "consistent":
				q.Acceptance.RepoID = "other"
				q = r.signed(t, q)
				want = cascade.KindIntegrity
			case "key":
				pub, _, _ := ed25519.GenerateKey(rand.Reader)
				q.SignerPubKeyB64 = base64.StdEncoding.EncodeToString(pub)
				q = r.signed(t, q)
			case "kinds":
				q.Acceptance.PlanKinds = []string{"bad:kind"}
				q = r.signed(t, q)
				want = cascade.KindInvalidInput
			}
			_, err := r.put(q)
			wantCIKind(t, err, want)
			r.assertEmpty(t)
		})
	}
}

func (r putRig) assertEmpty(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, err := r.rows.ByNonce(ctx, r.q.Acceptance.Nonce)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows after refusal: %+v %v", rows, err)
	}
	_, ok, err := r.nonces.AcceptedFor(ctx, r.q.Acceptance.Nonce, r.commit, r.sa.Record.TreeHash, r.q.Acceptance.CheckpointID)
	if err != nil || ok {
		t.Fatalf("acceptance after refusal: %v %v", ok, err)
	}
	_, ok, err = r.trust.Lookup(ctx, r.self.NodeID, r.sa.Record.KeyID)
	if err != nil || ok {
		t.Fatalf("vouch after refusal: %v %v", ok, err)
	}
}

// TestReceiveGateAcceptsAfterPut checks the node receive gate over a bare clone.
func TestReceiveGateAcceptsAfterPut(t *testing.T) {
	r := newPutRig(t)
	bare := filepath.Join(t.TempDir(), "gate.git")
	gitOutput(t, r.repo, "clone", "--bare", r.repo, bare)
	want := AttestWant{RepoID: r.q.Acceptance.RepoID, GateDir: bare, CommitSHA: r.commit, TreeHash: treeOf(t, bare, r.commit)}
	verify := func() error {
		return AttestVerify(context.Background(), want, true, r.rows, NewVouchedTrustStore(r.trust), r.nonces, nil, nil, r.clock.Now())
	}
	if err := verify(); !errors.Is(err, ErrNoAttestation) {
		t.Fatalf("before: %v", err)
	}
	if _, err := r.put(r.signed(t, r.q)); err != nil {
		t.Fatal(err)
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
}
