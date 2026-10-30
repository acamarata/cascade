package jobs

// Purpose: the jobs_delegation schema's tests (idempotent apply, the
//
//	triggers refusing raw UPDATE/DELETE/INSERT through the store's own db
//	handle) and the shared delegation fixtures: an in-memory envelope
//	signer seeded under t.TempDir and a chain builder.
//
// SPORT: jobs/delegation-record/ADD (P1-AGT-11).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

var dlgT0 = time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)

// dlgSigner is an in-memory envelope.Signer; it never touches a keychain.
type dlgSigner struct{ priv ed25519.PrivateKey }

func (d dlgSigner) PublicKey() ed25519.PublicKey { return d.priv.Public().(ed25519.PublicKey) }

func (d dlgSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(d.priv, msg), nil
}

// dlgFix is one store plus a signer and an unstored root envelope.
type dlgFix struct {
	t          *testing.T
	st         *Store
	sig        dlgSigner
	root       envelope.Envelope
	rootSigned []byte
}

func newDlgFix(t *testing.T) *dlgFix {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "controller.seed"), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t)
	if err := ApplyDelegationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyDelegationSchema: %v", err)
	}
	f := &dlgFix{t: t, st: NewStore(db), sig: dlgSigner{priv: ed25519.NewKeyFromSeed(seed)}}
	f.rootSigned, f.root = f.sign(dlgEnv("root", "owner-cli", ""))
	return f
}

func dlgEnv(id, principal, parentDigest string) envelope.Envelope {
	return envelope.Envelope{
		Version: envelope.Version, ID: id, ParentDigest: parentDigest, Principal: principal,
		Roots: []string{"/work"}, Tools: []string{"agent.tools.all"}, Commands: []string{},
		Egress: []string{"agent-driver"}, Grants: []envelope.GrantRef{},
		Budget:      envelope.Budget{WallClock: time.Hour, Tokens: 100, Procs: 2},
		Sensitivity: provider.SensitivityInternal, IssuedAt: dlgT0, ExpiresAt: dlgT0.Add(time.Hour),
	}
}

// sign issues e and returns its wire bytes and the issued envelope.
func (f *dlgFix) sign(e envelope.Envelope) ([]byte, envelope.Envelope) {
	f.t.Helper()
	s, err := envelope.Issue(context.Background(), f.sig, e)
	if err != nil {
		f.t.Fatalf("Issue: %v", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		f.t.Fatal(err)
	}
	return b, s.Env
}

// childOf returns a record for a fresh child of parent named id.
func (f *dlgFix) childOf(parent envelope.Envelope, id string) (DelegationRecord, envelope.Envelope) {
	f.t.Helper()
	d, err := envelope.Digest(parent)
	if err != nil {
		f.t.Fatal(err)
	}
	b, env := f.sign(dlgEnv(id, "agent-"+id, d))
	return DelegationRecord{ID: id, Signed: b, AgentPrincipal: env.Principal, JobID: "job-1",
		State: DelegationLive, CreatedAt: dlgT0}, env
}

// add inserts a child of parent (a first hop when parent is the root).
func (f *dlgFix) add(parent envelope.Envelope, id string) envelope.Envelope {
	f.t.Helper()
	r, env := f.childOf(parent, id)
	if parent.ID == f.root.ID {
		r.ParentSigned = f.rootSigned
	}
	if err := f.st.InsertDelegation(context.Background(), r); err != nil {
		f.t.Fatalf("InsertDelegation(%s): %v", id, err)
	}
	return env
}

func (f *dlgFix) get(id string) DelegationRecord {
	f.t.Helper()
	r, err := f.st.GetDelegation(context.Background(), id)
	if err != nil {
		f.t.Fatalf("GetDelegation(%s): %v", id, err)
	}
	return r
}

// wantErr asserts err's Kind and that its message names want.
func wantErr(t *testing.T, err error, kind cascade.Kind, want string) {
	t.Helper()
	if err == nil || !cascade.HasKind(err, kind) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want Kind %s naming %q", err, kind, want)
	}
}

// wantRaw asserts a raw statement was refused by the named trigger.
func wantRaw(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("raw statement: err = %v, want %q", err, msg)
	}
}

// rawExec runs one statement through the store's own db handle.
func (f *dlgFix) rawExec(q string, args ...any) error {
	_, err := f.st.db.ExecContext(context.Background(), q, args...)
	return err
}

func TestDelegationSchemaMigration(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	if err := ApplyDelegationSchema(ctx, f.st.db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	var n int
	if err := f.st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`,
		tableDelegation).Scan(&n); err != nil || n != len(delegationTriggers) || n != 8 {
		t.Fatalf("triggers = %d (%v), want 8", n, err)
	}
	for _, idx := range []string{"jobs_delegation_digest_idx", "jobs_delegation_agent_idx", "jobs_delegation_parent_state_idx"} {
		if err := f.st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`,
			idx).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s = %d (%v)", idx, n, err)
		}
	}
	wantErr(t, ApplyDelegationSchema(ctx, f.st.db, migrate.PostgresEmitter{}, newTestClock(), "", ""), cascade.KindUnsupported, "SQLite only")
	wantErr(t, ApplyDelegationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""), cascade.KindInvalidInput, "non-nil db")
	wantErr(t, ApplyDelegationSchema(ctx, f.st.db, migrate.SQLiteEmitter{}, nil, "", ""), cascade.KindInvalidInput, "non-nil Clock")
}

func TestDelegationEnvelopeStoreImmutable(t *testing.T) {
	f := newDlgFix(t)
	a := f.add(f.root, "a")
	f.add(a, "b")
	before := f.get("b")
	frozen := []string{"id", "envelope_digest", "signed", "parent_digest", "parent_signed", "origin_principal",
		"origin_session", "agent_principal", "depth", "job_id", "mutation", "lane", "created_at"}
	if len(frozen) != 13 {
		t.Fatalf("frozen columns = %d, want 13", len(frozen))
	}
	for _, col := range frozen {
		err := f.rawExec(`UPDATE jobs_delegation SET ` + col + ` = '1' WHERE id = 'b'`)
		if err == nil || !strings.Contains(err.Error(), msgDelegationImmutable) {
			t.Fatalf("raw UPDATE of %s: err = %v, want %q", col, err, msgDelegationImmutable)
		}
	}
	if err := f.rawExec(`DELETE FROM jobs_delegation WHERE id = 'b'`); err == nil || !strings.Contains(err.Error(), msgDelegationNoDelete) {
		t.Fatalf("raw DELETE: err = %v", err)
	}
	ctx := context.Background()
	if err := f.st.BindDelegationMarker(ctx, "b", "clm_1"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.BindDelegationSpawn(ctx, "b", "lease-1", "ajob-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishDelegation(ctx, "b", DelegationSucceeded, "done"); err != nil {
		t.Fatal(err)
	}
	after := f.get("b")
	if string(after.Signed) != string(before.Signed) || string(after.ParentSigned) != string(before.ParentSigned) ||
		after.EnvelopeDigest != before.EnvelopeDigest || after.OriginPrincipal != before.OriginPrincipal || len(before.Signed) == 0 {
		t.Fatalf("an exported path changed a record field: before %+v after %+v", before, after)
	}
}

func TestDelegationInsertTriggerRefusesNonLive(t *testing.T) {
	f := newDlgFix(t)
	f.add(f.root, "a")
	err := f.rawExec(`INSERT INTO jobs_delegation (` + delegationCols + `)
		SELECT 'z', 'dz', signed, parent_digest, parent_signed, origin_principal, origin_session, 'agent-z', depth,
		job_id, mutation, lane, NULL, NULL, NULL, 'succeeded', '', created_at, updated_at FROM jobs_delegation WHERE id = 'a'`)
	if err == nil || !strings.Contains(err.Error(), msgDelegationState) {
		t.Fatalf("raw non-live INSERT: err = %v, want %q", err, msgDelegationState)
	}
	if err := f.rawExec(`UPDATE jobs_delegation SET state = 'bogus' WHERE id = 'a'`); err == nil ||
		!strings.Contains(err.Error(), msgDelegationState) {
		t.Fatalf("raw unknown state: err = %v", err)
	}
	if err := f.rawExec(`UPDATE jobs_delegation SET lease_marker = 'm', lease_id = 'l' WHERE id = 'a'`); err == nil ||
		!strings.Contains(err.Error(), msgDelegationState) {
		t.Fatalf("raw lease without agent job: err = %v", err)
	}
	if err := f.rawExec(`UPDATE jobs_delegation SET lease_id = 'l', agent_job_id = 'j' WHERE id = 'a'`); err == nil ||
		!strings.Contains(err.Error(), msgDelegationMarkFirst) {
		t.Fatalf("raw spawn before marker: err = %v", err)
	}
}
