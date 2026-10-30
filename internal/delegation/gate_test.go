package delegation

// Purpose: the pre-spawn gate registry's tests, and the shared store
//
//	fixture: a real jobs store (a modernc-sqlite file under t.TempDir)
//	with an unstored owner-cli root and chain builders.
//
// SPORT: delegation/gate/ADD (P1-AGT-11).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/envelope"

	_ "modernc.org/sqlite" // the "sqlite" database/sql driver for the real store
)

// chain is one store, one signer and an unstored owner-cli root.
type chain struct {
	t     *testing.T
	db    *sql.DB
	st    *jobs.Store
	s     testSigner
	root  envelope.Signed
	rootB []byte
	n     int
}

func newChain(t *testing.T) *chain {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := jobs.ApplyDelegationSchema(context.Background(), db, migrate.SQLiteEmitter{}, runtime.NewFixedClock(t0), "", ""); err != nil {
		t.Fatal(err)
	}
	c := &chain{t: t, db: db, st: jobs.NewStore(db), s: newSigner(t)}
	c.root, err = MintRoot(context.Background(), c.s, principal.Principal{ID: OwnerCLI}, srcMut())
	if err != nil {
		t.Fatal(err)
	}
	c.rootB = c.wire(c.root)
	return c
}

func (c *chain) wire(s envelope.Signed) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		c.t.Fatal(err)
	}
	return b
}

// insert stores signed (with parentB for a first hop) and returns the row.
func (c *chain) insert(signed []byte, env envelope.Envelope, parentB []byte) (jobs.DelegationRecord, error) {
	err := c.st.InsertDelegation(context.Background(), jobs.DelegationRecord{ID: env.ID, Signed: signed,
		ParentSigned: parentB, AgentPrincipal: env.Principal, JobID: "job-1", State: jobs.DelegationLive, CreatedAt: t0})
	if err != nil {
		return jobs.DelegationRecord{}, err
	}
	return c.st.GetDelegation(context.Background(), env.ID)
}

// add mints a child of parent through MintChild and stores it.
func (c *chain) add(parent envelope.Signed) (envelope.Signed, jobs.DelegationRecord) {
	c.t.Helper()
	c.n++
	child, err := MintChild(context.Background(), c.s, parent, c.s.PublicKey(), fmt.Sprintf("agent-%d", c.n), srcMut())
	if err != nil {
		c.t.Fatal(err)
	}
	var pb []byte
	if parent.Env.ID == c.root.Env.ID {
		pb = c.rootB
	}
	rec, err := c.insert(c.wire(child), child.Env, pb)
	if err != nil {
		c.t.Fatal(err)
	}
	return child, rec
}

// tamper flips one byte of the signature inside a Signed wire form.
func tamper(t *testing.T, b []byte) []byte {
	t.Helper()
	s, err := envelope.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	s.Sig = append([]byte(nil), s.Sig...)
	s.Sig[0] ^= 1
	out, _ := json.Marshal(s)
	return out
}

func (c *chain) verify(rec jobs.DelegationRecord, now time.Time) (envelope.Envelope, error) {
	return VerifyChain(context.Background(), c.st, rec, c.s.PublicKey(), now)
}

func (c *chain) mustVerify(rec jobs.DelegationRecord) {
	c.t.Helper()
	env, err := c.verify(rec, t0)
	if err != nil || env.ID != rec.ID {
		c.t.Fatalf("VerifyChain(%s) = %s, %v; want it to verify", rec.ID, env.ID, err)
	}
}

// rawRow inserts a live row straight into the table, as a write to the
// database file behind the store's back would.
func (c *chain) rawRow(id, digest string, signed []byte, parentDigest string, parentSigned []byte, depth int) jobs.DelegationRecord {
	c.t.Helper()
	_, err := c.db.ExecContext(context.Background(), `INSERT INTO jobs_delegation (id, envelope_digest, signed,
		parent_digest, parent_signed, origin_principal, origin_session, agent_principal, depth, job_id, mutation, lane,
		state, reason, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'owner-cli', '', ?, ?, 'job-1', 0, '', 'live', '', 1, 1)`,
		id, digest, signed, parentDigest, parentSigned, "agent-raw-"+id, depth)
	if err != nil {
		c.t.Fatalf("raw insert %s: %v", id, err)
	}
	rec, err := c.st.GetDelegation(context.Background(), id)
	if err != nil {
		c.t.Fatal(err)
	}
	return rec
}

// isolateGates gives the test an empty registry and restores the old one.
func isolateGates(t *testing.T) {
	t.Helper()
	gateRegistry.Lock()
	saved := gateRegistry.m
	gateRegistry.m = map[string]PreSpawnGate{}
	gateRegistry.Unlock()
	t.Cleanup(func() {
		gateRegistry.Lock()
		gateRegistry.m = saved
		gateRegistry.Unlock()
	})
}

func wantPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if s, _ := r.(string); !strings.Contains(s, want) {
			t.Fatalf("panic = %v, want one naming %q", r, want)
		}
	}()
	fn()
}

func TestPreSpawnGateRegistration(t *testing.T) {
	isolateGates(t)
	var ran []string
	gate := func(name string) PreSpawnGate {
		return func(context.Context, jobs.DelegationRecord, envelope.Envelope) error {
			ran = append(ran, name)
			return nil
		}
	}
	for _, n := range []string{"budget", "audit", "quota"} {
		if done := RegisterPreSpawnGate(n, gate(n)); done != (struct{}{}) {
			t.Fatalf("RegisterPreSpawnGate(%s) = %v", n, done)
		}
	}
	for round := 0; round < 2; round++ {
		ran = nil
		gates := PreSpawnGates()
		for _, g := range gates {
			if err := g(context.Background(), jobs.DelegationRecord{}, envelope.Envelope{}); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Join(ran, ",") != "audit,budget,quota" {
			t.Fatalf("round %d ran %v, want audit,budget,quota", round, ran)
		}
	}
	wantPanic(t, "pre-spawn gate budget is already registered", func() { RegisterPreSpawnGate("budget", gate("x")) })
	wantPanic(t, "needs a name and a gate", func() { RegisterPreSpawnGate("", gate("x")) })
	wantPanic(t, "needs a name and a gate", func() { RegisterPreSpawnGate("nil", nil) })
	if n := len(PreSpawnGates()); n != 3 {
		t.Fatalf("a refused registration changed the registry: %d gates", n)
	}
}
