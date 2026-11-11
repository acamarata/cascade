// Purpose: exercise receive-gate binding, replay and passing-run checks.
// Constraints: temporary homes and databases; signing keys stay in memory.
package ci

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	gateCommit = "1111111111111111111111111111111111111111"
	gateTree   = "2222222222222222222222222222222222222222"
)

type gateTrust struct {
	nodeID string
	pub    ed25519.PublicKey
}

func (g gateTrust) Lookup(_ context.Context, nodeID, keyID string) (ed25519.PublicKey, error) {
	if nodeID != g.nodeID || keyID != nodes.KeyIDFor(g.pub) {
		return nil, attest.ErrRevokedKey
	}
	return g.pub, nil
}

type gateFacts map[string]jobs.AcceptanceFact

func (f gateFacts) AcceptedFor(_ context.Context, nonce, commit, tree, checkpoint string) (jobs.AcceptanceFact, bool, error) {
	fact, ok := f[nonce]
	if !ok || fact.Commit != commit || fact.Tree != tree || fact.CheckpointID != checkpoint {
		return jobs.AcceptanceFact{}, false, nil
	}
	return fact, true, nil
}

// looseGateFact lets the verifier itself check every bound field, including checkpoint.
type looseGateFact struct {
	fact jobs.AcceptanceFact
	err  error
}

func (f looseGateFact) AcceptedFor(context.Context, string, string, string, string) (jobs.AcceptanceFact, bool, error) {
	return f.fact, true, f.err
}

type staticGate string

func (g staticGate) GateRepoID(context.Context, string) (string, error) { return string(g), nil }

type errGate struct{ err error }

func (g errGate) GateRepoID(context.Context, string) (string, error) { return "", g.err }

type gateRig struct {
	db    *sql.DB
	store *jobs.CiAttestationStore
	trust gateTrust
	priv  ed25519.PrivateKey
	facts gateFacts
	now   time.Time
}

func gateOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func gateError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("verify = %v, want %v", err, want)
	}
}
func newGateRig(t *testing.T) gateRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	clock := newTestClock()
	db := openFileDB(t, "gate.db")
	gateOK(t, jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""))
	store, err := jobs.NewCiAttestationStore(db)
	gateOK(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	gateOK(t, err)
	return gateRig{db: db, store: store, trust: gateTrust{nodeID: "node-a", pub: pub}, priv: priv, facts: gateFacts{}, now: clock.Now()}
}
func (r gateRig) record(class attest.AttestorClass) attest.AttestationRecord {
	return attest.AttestationRecord{
		SchemaVersion: attest.AttestationSchemaVersion, AttestorClass: class, CheckpointID: "cp-1",
		CommitSHA: gateCommit, TreeHash: gateTree, NodeID: r.trust.nodeID, KeyID: nodes.KeyIDFor(r.trust.pub),
		Nonce: "nonce-1", PolicyVersion: "policy-1", RepoID: "repo-1", JobSpecHash: "spec-1",
		ResultHashes: []string{"unit:abc"}, StartedAt: r.now.Add(-time.Hour), EndedAt: r.now.Add(-30 * time.Minute), ExpiresAt: r.now.Add(time.Hour),
	}
}
func (r gateRig) insert(t *testing.T, rec attest.AttestationRecord, signer ed25519.PrivateKey) {
	t.Helper()
	sa, err := attest.Attest(rec, func(p []byte) ([]byte, error) { return ed25519.Sign(signer, p), nil })
	gateOK(t, err)
	row := localAttestationRow(sa, r.now)
	row.AttestorClass = string(attest.ParseAttestorClass(string(rec.AttestorClass)))
	gateOK(t, r.store.Insert(context.Background(), row))
	r.facts[rec.Nonce] = jobs.AcceptanceFact{Nonce: rec.Nonce, CheckpointID: rec.CheckpointID, RepoID: rec.RepoID,
		JobSpecHash: rec.JobSpecHash, PolicyVersion: rec.PolicyVersion, Commit: rec.CommitSHA, Tree: rec.TreeHash, PlanKinds: []string{"unit"}}
}
func (r gateRig) call(gates GateBinding, policy func() (string, error), acc AcceptanceLookup) error {
	want := AttestWant{RepoID: "repo-1", GateDir: "/srv/repo.git", CommitSHA: gateCommit, TreeHash: gateTree}
	return AttestVerify(context.Background(), want, true, r.store, r.trust, acc, gates, policy, r.now)
}
func (r gateRig) verify(require bool) error {
	want := AttestWant{RepoID: "repo-1", GateDir: "/srv/repo.git", CommitSHA: gateCommit, TreeHash: gateTree}
	return AttestVerify(context.Background(), want, require, r.store, r.trust, r.facts, staticGate("repo-1"), gatePolicy, r.now)
}
func gatePolicy() (string, error) { return "policy-1", nil }

func TestAttestVerifyErrors(t *testing.T) {
	for _, tc := range []struct {
		name            string
		class           attest.AttestorClass
		missing, badKey bool
		want            error
	}{
		{"accepted", attest.ClassEnrolledNode, false, false, nil},
		{"signature mismatch", attest.ClassEnrolledNode, false, true, ErrVerifyFailed},
		{"unattested", attest.ClassUnattested, false, false, ErrUnattestedEvidence},
		{"unverified", attest.ClassUnverified, false, false, ErrUnattestedEvidence},
		{"no acceptance fact", attest.ClassEnrolledNode, true, false, ErrUnattestedEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGateRig(t)
			signer := r.priv
			if tc.badKey {
				signer = newGateRig(t).priv
			}
			r.insert(t, r.record(tc.class), signer)
			if tc.missing {
				delete(r.facts, "nonce-1")
			}
			gateError(t, r.verify(true), tc.want)
		})
	}
	r := newGateRig(t)
	gateError(t, r.verify(true), ErrNoAttestation)
	gateOK(t, r.verify(false))
	gateError(t, r.call(nil, nil, r.facts), ErrNoAttestation)
}
func TestAttestVerifyInputsAndBinding(t *testing.T) {
	r := newGateRig(t)
	for _, want := range []AttestWant{{RepoID: "repo-1", TreeHash: gateTree}, {RepoID: "repo-1", CommitSHA: gateCommit}, {CommitSHA: gateCommit, TreeHash: gateTree}} {
		if err := AttestVerify(context.Background(), want, true, r.store, r.trust, r.facts, nil, nil, r.now); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatal(err)
		}
	}
	want := AttestWant{RepoID: "repo-1", CommitSHA: gateCommit, TreeHash: gateTree}
	if err := AttestVerify(context.Background(), want, true, nil, r.trust, r.facts, nil, nil, r.now); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatal(err)
	}
	for _, gates := range []GateBinding{nil, staticGate("repo-2")} {
		if err := r.call(gates, gatePolicy, r.facts); !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatal(err)
		}
	}
	boom := errors.New("lookup failed")
	gateError(t, r.call(errGate{boom}, gatePolicy, r.facts), boom)
	r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
	gateError(t, r.call(nil, nil, looseGateFact{err: boom}), boom)
}
func TestGitGateChecksBoundFields(t *testing.T) {
	for _, name := range []string{"accepted", "record repo", "checkpoint", "job spec", "record policy", "fact repo", "obsolete policy", "policy error", "expired", "revoked key", "empty plan", "unproven kind", "fact commit", "fact tree", "node policy"} {
		t.Run(name, func(t *testing.T) { checkGateBoundField(t, name) })
	}
}
func checkGateBoundField(t *testing.T, name string) {
	r := newGateRig(t)
	rec := r.record(attest.ClassEnrolledNode)
	if name == "expired" {
		rec.ExpiresAt = r.now
	}
	if name == "record repo" || name == "fact repo" {
		rec.RepoID = "other"
	}
	r.insert(t, rec, r.priv)
	fact, policy, wantErr := r.facts[rec.Nonce], gatePolicy, error(ErrUnattestedEvidence)
	mutations := map[string]func(){
		"record repo":   func() { fact.RepoID = "repo-1" },
		"checkpoint":    func() { fact.CheckpointID = "other" },
		"job spec":      func() { fact.JobSpecHash = "other" },
		"record policy": func() { fact.PolicyVersion = "other" },
		"empty plan":    func() { fact.PlanKinds = nil },
		"unproven kind": func() { fact.PlanKinds = []string{"unit", "e2e"} },
		"fact commit":   func() { fact.Commit = "other" },
		"fact tree":     func() { fact.Tree = "other" },
	}
	if mutate := mutations[name]; mutate != nil {
		mutate()
	}
	switch name {
	case "obsolete policy":
		policy = func() (string, error) { return "policy-2", nil }
	case "policy error":
		wantErr = errors.New("policy unreadable")
		policy = func() (string, error) { return "", wantErr }
	case "expired":
		wantErr = ErrVerifyFailed
	case "revoked key":
		r.trust.nodeID = "revoked"
		wantErr = ErrVerifyFailed
	case "accepted":
		wantErr = nil
	case "node policy":
		policy = nil
		wantErr = nil
	}
	err := r.call(staticGate("repo-1"), policy, looseGateFact{fact: fact})
	gateError(t, err, wantErr)
	if name == "policy error" && !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatal(err)
	}
}

type gateSigner struct{ key ed25519.PrivateKey }

func (s gateSigner) PublicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }
func (s gateSigner) Sign(_ context.Context, p []byte) ([]byte, error) {
	return ed25519.Sign(s.key, p), nil
}

// acceptedGateRecord consumes only during fixture setup; verification must remain read-only.
func acceptedGateRecord(t *testing.T, r gateRig, rec attest.AttestationRecord) *jobs.AttestationNonceStore {
	t.Helper()
	ctx := context.Background()
	nonces, err := jobs.NewAttestationNonceStore(r.db, newTestClock())
	gateOK(t, err)
	rec.Nonce, _, err = nonces.Issue(ctx, "job-1", rec.CheckpointID, rec.RepoID, rec.JobSpecHash, rec.PolicyVersion, []string{"unit"}, time.Hour)
	gateOK(t, err)
	tx, err := r.db.BeginTx(ctx, nil)
	gateOK(t, err)
	defer func() { _ = tx.Rollback() }()
	gateOK(t, nonces.ConsumeOnAcceptTx(ctx, tx, rec.Nonce, "job-1", rec.CommitSHA, rec.TreeHash, r.now))
	gateOK(t, tx.Commit())
	r.insert(t, rec, r.priv)
	return nonces
}
func TestGitGateRejectsCrossRepoReplay(t *testing.T) {
	r := newGateRig(t)
	ctx, clock := context.Background(), newTestClock()
	gateOK(t, jobs.ApplyRepoRegistrySchema(ctx, r.db, migrate.SQLiteEmitter{}, clock, "", ""))
	reg, err := jobs.NewRepoRegistry(r.db, scope.NewGraphStore(r.db), contextgit.GitCommonDir, clock)
	gateOK(t, err)
	reg, err = reg.WithTrustRecordSigner(gateSigner{r.priv})
	gateOK(t, err)
	var ids, repos []string
	for i := 0; i < 2; i++ {
		repo := baselineRepo(t)
		id, err := reg.Register(ctx, repo, "", func(context.Context, string) ([]jobs.BranchTip, error) { return nil, nil })
		gateOK(t, err)
		gateOK(t, BindGateRepoID(ctx, repo, id, reg))
		ids, repos = append(ids, id), append(repos, repo)
	}
	if ids[0] == ids[1] {
		t.Fatal("registrations must differ")
	}
	rec := r.record(attest.ClassEnrolledNode)
	rec.RepoID = ids[0]
	nonces := acceptedGateRecord(t, r, rec)
	for i, repo := range repos {
		want, err := ResolveGateWant(ctx, repo, AttestWant{GateDir: repo, CommitSHA: gateCommit, TreeHash: gateTree}, reg)
		gateOK(t, err)
		err = AttestVerify(ctx, want, true, r.store, r.trust, nonces, reg, gatePolicy, r.now)
		if i == 0 {
			gateOK(t, err)
		} else {
			gateError(t, err, ErrUnattestedEvidence)
		}
	}
}
func TestGitGateRejectsFailingRun(t *testing.T) {
	r := newGateRig(t)
	rec := r.record(attest.ClassEnrolledNode)
	rec.ExitStatus = 1
	nonces := acceptedGateRecord(t, r, rec)
	for i := 0; i < 2; i++ {
		gateError(t, r.call(staticGate("repo-1"), gatePolicy, nonces), ErrUnattestedEvidence)
	}
}
