package ci

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
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

type gateRig struct {
	store *jobs.CiAttestationStore
	trust gateTrust
	priv  ed25519.PrivateKey
	facts gateFacts
	now   time.Time
}

func newGateRig(t *testing.T) gateRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	clock := newTestClock()
	db := openFileDB(t, "gate.db")
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	store, err := jobs.NewCiAttestationStore(db)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return gateRig{store: store, trust: gateTrust{nodeID: "node-a", pub: pub}, priv: priv, facts: gateFacts{}, now: clock.Now()}
}

func (r gateRig) record(class attest.AttestorClass) attest.AttestationRecord {
	return attest.AttestationRecord{
		SchemaVersion: attest.AttestationSchemaVersion, AttestorClass: class, CheckpointID: "cp-1",
		CommitSHA: gateCommit, TreeHash: gateTree, NodeID: r.trust.nodeID, KeyID: nodes.KeyIDFor(r.trust.pub),
		Nonce: "nonce-1", PolicyVersion: "policy-1", RepoID: "repo-1", JobSpecHash: "spec-1",
		ResultHashes: []string{"unit:abc"}, StartedAt: r.now.Add(-time.Hour), EndedAt: r.now.Add(-30 * time.Minute),
		ExpiresAt: r.now.Add(time.Hour),
	}
}

// insert signs rec with signer, stores it under class and records the
// matching acceptance fact.
func (r gateRig) insert(t *testing.T, rec attest.AttestationRecord, signer ed25519.PrivateKey) {
	t.Helper()
	sa, err := attest.Attest(rec, func(p []byte) ([]byte, error) { return ed25519.Sign(signer, p), nil })
	if err != nil {
		t.Fatal(err)
	}
	row := localAttestationRow(sa, r.now)
	row.AttestorClass = string(attest.ParseAttestorClass(string(rec.AttestorClass)))
	if err := r.store.Insert(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	r.facts[rec.Nonce] = jobs.AcceptanceFact{Nonce: rec.Nonce, CheckpointID: rec.CheckpointID, RepoID: rec.RepoID,
		JobSpecHash: rec.JobSpecHash, PolicyVersion: rec.PolicyVersion, Commit: rec.CommitSHA, Tree: rec.TreeHash,
		PlanKinds: []string{"unit"}}
}

func (r gateRig) verify(require bool) error {
	want := AttestWant{RepoID: "repo-1", GateDir: "/srv/repo.git", CommitSHA: gateCommit, TreeHash: gateTree}
	policy := func() (string, error) { return "policy-1", nil }
	return AttestVerify(context.Background(), want, require, r.store, r.trust, r.facts, staticGate("repo-1"), policy, r.now)
}

type staticGate string

func (g staticGate) GateRepoID(context.Context, string) (string, error) { return string(g), nil }

func TestAttestVerifyErrors(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		r := newGateRig(t)
		r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
		if err := r.verify(true); err != nil {
			t.Fatalf("AttestVerify(attested tip) = %v, want nil", err)
		}
	})
	t.Run("require with zero rows", func(t *testing.T) {
		r := newGateRig(t)
		if err := r.verify(true); err != ErrNoAttestation {
			t.Fatalf("AttestVerify(no rows, require) = %v, want ErrNoAttestation", err)
		}
	})
	t.Run("signature mismatch", func(t *testing.T) {
		r := newGateRig(t)
		_, other, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		r.insert(t, r.record(attest.ClassEnrolledNode), other)
		err = r.verify(true)
		if !errors.Is(err, ErrVerifyFailed) || errors.Is(err, ErrUnattestedEvidence) {
			t.Fatalf("AttestVerify(signed by another key) = %v, want ErrVerifyFailed", err)
		}
	})
	for _, class := range []attest.AttestorClass{attest.ClassUnattested, attest.ClassUnverified} {
		t.Run(string(class), func(t *testing.T) {
			r := newGateRig(t)
			r.insert(t, r.record(class), r.priv)
			err := r.verify(true)
			if !errors.Is(err, ErrUnattestedEvidence) || errors.Is(err, ErrVerifyFailed) {
				t.Fatalf("AttestVerify(%s) = %v, want ErrUnattestedEvidence", class, err)
			}
		})
	}
	t.Run("no acceptance fact", func(t *testing.T) {
		r := newGateRig(t)
		r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
		delete(r.facts, "nonce-1")
		if err := r.verify(true); !errors.Is(err, ErrUnattestedEvidence) {
			t.Fatalf("AttestVerify(no acceptance fact) = %v, want ErrUnattestedEvidence", err)
		}
	})
}

type errFacts struct{ err error }

func (e errFacts) AcceptedFor(context.Context, string, string, string, string) (jobs.AcceptanceFact, bool, error) {
	return jobs.AcceptanceFact{}, false, e.err
}

type errGate struct{ err error }

func (e errGate) GateRepoID(context.Context, string) (string, error) { return "", e.err }

func (r gateRig) call(gates GateBinding, policy func() (string, error), acc AcceptanceLookup) error {
	want := AttestWant{RepoID: "repo-1", GateDir: "/srv/repo.git", CommitSHA: gateCommit, TreeHash: gateTree}
	return AttestVerify(context.Background(), want, true, r.store, r.trust, acc, gates, policy, r.now)
}

func TestAttestVerifyInputsAndBinding(t *testing.T) {
	r := newGateRig(t)
	good := func() (string, error) { return "policy-1", nil }
	for _, tc := range []struct {
		name string
		want AttestWant
		nils bool
	}{
		{"nil dependencies", AttestWant{RepoID: "repo-1", CommitSHA: gateCommit, TreeHash: gateTree}, true},
		{"no commit", AttestWant{RepoID: "repo-1", TreeHash: gateTree}, false},
		{"no tree", AttestWant{RepoID: "repo-1", CommitSHA: gateCommit}, false},
		{"no repo id", AttestWant{CommitSHA: gateCommit, TreeHash: gateTree}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := r.store
			if tc.nils {
				store = nil
			}
			err := AttestVerify(context.Background(), tc.want, true, store, r.trust, r.facts, nil, nil, r.now)
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("AttestVerify = %v, want KindInvalidInput", err)
			}
		})
	}
	t.Run("controller without gate binding", func(t *testing.T) {
		if err := r.call(nil, good, r.facts); !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("call(nil gates, policy) = %v, want KindPolicyDenied", err)
		}
	})
	t.Run("gate lookup error", func(t *testing.T) {
		boom := errors.New("registry down")
		if err := r.call(errGate{boom}, good, r.facts); !errors.Is(err, boom) {
			t.Fatalf("call(gate error) = %v, want %v", err, boom)
		}
	})
	t.Run("gate binding disagrees", func(t *testing.T) {
		if err := r.call(staticGate("repo-2"), good, r.facts); !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("call(other repo) = %v, want KindPolicyDenied", err)
		}
	})
	t.Run("node role not required zero rows", func(t *testing.T) {
		if err := r.call(nil, nil, r.facts); !errors.Is(err, ErrNoAttestation) {
			t.Fatalf("call(node, no rows) = %v, want ErrNoAttestation", err)
		}
		want := AttestWant{RepoID: "repo-1", CommitSHA: gateCommit, TreeHash: gateTree}
		if err := AttestVerify(context.Background(), want, false, r.store, r.trust, r.facts, nil, nil, r.now); err != nil {
			t.Fatalf("AttestVerify(not required, no rows) = %v, want nil", err)
		}
	})
}

func TestAttestVerifyFactBinding(t *testing.T) {
	boom := errors.New("lookup failed")
	policyErr := errors.New("policy unreadable")
	for _, tc := range []struct {
		name   string
		mutate func(f *jobs.AcceptanceFact)
		policy func() (string, error)
		denied bool
		is     error
	}{
		{"repo id differs", func(f *jobs.AcceptanceFact) { f.RepoID = "repo-2" }, nil, false, ErrUnattestedEvidence},
		{"job spec differs", func(f *jobs.AcceptanceFact) { f.JobSpecHash = "other" }, nil, false, ErrUnattestedEvidence},
		{"policy differs from record", func(f *jobs.AcceptanceFact) { f.PolicyVersion = "policy-0" }, nil, false, ErrUnattestedEvidence},
		{"policy not current", func(*jobs.AcceptanceFact) {}, func() (string, error) { return "policy-2", nil }, false, ErrUnattestedEvidence},
		{"policy unreadable", func(*jobs.AcceptanceFact) {}, func() (string, error) { return "", policyErr }, true, policyErr},
		{"run did not pass", func(f *jobs.AcceptanceFact) { f.PlanKinds = nil }, nil, false, ErrUnattestedEvidence},
		{"unproven plan kind", func(f *jobs.AcceptanceFact) { f.PlanKinds = []string{"unit", "e2e"} }, nil, false, ErrUnattestedEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGateRig(t)
			r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
			f := r.facts["nonce-1"]
			tc.mutate(&f)
			r.facts["nonce-1"] = f
			err := r.call(staticGate("repo-1"), tc.policy, r.facts)
			if err == nil || !errors.Is(err, tc.is) || (tc.denied && !cascade.HasKind(err, cascade.KindPolicyDenied)) {
				t.Fatalf("call = %v, want %v", err, tc.is)
			}
		})
	}
	t.Run("acceptance lookup error", func(t *testing.T) {
		r := newGateRig(t)
		r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
		if err := r.call(nil, nil, errFacts{boom}); !errors.Is(err, boom) {
			t.Fatalf("call(lookup error) = %v, want %v", err, boom)
		}
	})
	t.Run("current policy accepted", func(t *testing.T) {
		r := newGateRig(t)
		r.insert(t, r.record(attest.ClassEnrolledNode), r.priv)
		if err := r.call(staticGate("repo-1"), func() (string, error) { return "policy-1", nil }, r.facts); err != nil {
			t.Fatalf("call(current policy) = %v, want nil", err)
		}
	})
}
