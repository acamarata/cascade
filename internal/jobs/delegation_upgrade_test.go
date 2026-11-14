package jobs

// Purpose: a version 1 jobs_delegation database opens under version 2: the
// lease id columns are added in place, the changed triggers are replaced, a
// version 1 row stays readable and finishable, and a version 1 binary refuses
// the upgraded database.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"io"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// delegationV1Set is the version 1 set as it shipped: the same steps, version 1.
func delegationV1Set() migrate.MigrationSet {
	set := DelegationMigrationSet()
	set.SchemaVersion, set.ReaderCeiling = 1, 1
	return set
}

// delegationV1Immutable is the version 1 body of jobs_delegation_immutable.
const delegationV1Immutable = `CREATE TRIGGER jobs_delegation_immutable BEFORE UPDATE ON jobs_delegation
	 WHEN NEW.id IS NOT OLD.id OR NEW.signed IS NOT OLD.signed
	 BEGIN SELECT RAISE(ABORT, 'jobs_delegation: record fields are immutable'); END`

// openDelegationV1 opens a database at version 1 holding one spawned live row "old".
func openDelegationV1(ctx context.Context, t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}, delegationV1Set())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{delegationV1Immutable, `INSERT INTO jobs_delegation (id, envelope_digest, signed, parent_digest,
		origin_principal, origin_session, agent_principal, depth, job_id, mutation, lane, lease_id, agent_job_id,
		lease_marker, state, reason, created_at, updated_at) VALUES ('old', 'd-old', x'7b7d', 'p', 'owner-cli', '',
		'agent-old', 1, '', 0, 'agent:drv-1', 'lease-0', 'aj-0', 'clm_0', 'live', '', 1, 1)`} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestDelegationSchemaUpgradesV1(t *testing.T) {
	ctx := context.Background()
	db := openDelegationV1(ctx, t)
	for range 2 { // the second apply is a no-op
		if err := ApplyDelegationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
	}
	var version, triggers int
	if err := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM applied_migrations WHERE set_id = 'jobs-delegation'`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema_version = %d (%v), want 2", version, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`,
		tableDelegation).Scan(&triggers); err != nil || triggers != len(delegationTriggers) {
		t.Fatalf("triggers = %d (%v), want %d", triggers, err, len(delegationTriggers))
	}
	st := NewStore(db)
	old, err := st.GetDelegation(ctx, "old")
	if err != nil || old.LeaseID != "lease-0" || old.ExecutionID != "" || old.ReservationID != "" {
		t.Fatalf("version 1 row = %+v, %v; want its lease and no ids", old, err)
	}
	if err := st.FinishDelegation(ctx, "old", DelegationFailed, "daemon restart"); err != nil {
		t.Fatalf("finish a version 1 row: %v", err)
	}
	upgradedRowRoundTrips(ctx, t, st)
	err = migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}, delegationV1Set())
	if err == nil {
		t.Fatal("a version 1 binary opened the version 2 database")
	}
}

// upgradedRowRoundTrips stores the lease ids with a new row and proves the
// replaced triggers hold them immutable and both-or-neither.
func upgradedRowRoundTrips(ctx context.Context, t *testing.T, st *Store) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		t.Fatal(err)
	}
	f := &dlgFix{t: t, st: st, sig: dlgSigner{priv: ed25519.NewKeyFromSeed(seed)}}
	f.rootSigned, f.root = f.sign(dlgEnv("root", "owner-cli", ""))
	r, _ := f.childOf(f.root, "new")
	r.ParentSigned, r.ExecutionID = f.rootSigned, "exec-1"
	wantErr(t, st.InsertDelegation(ctx, r), cascade.KindInvalidInput, "set together")
	r.ReservationID = "rsv-1"
	if err := st.InsertDelegation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := f.get("new"); got.ExecutionID != "exec-1" || got.ReservationID != "rsv-1" {
		t.Fatalf("stored ids = %q %q, want exec-1 rsv-1", got.ExecutionID, got.ReservationID)
	}
	wantRaw(t, f.rawExec(`UPDATE jobs_delegation SET execution_id = 'exec-2' WHERE id = 'new'`), msgDelegationImmutable)
	wantRaw(t, f.rawExec(`UPDATE jobs_delegation SET reservation_id = NULL WHERE id = 'new'`), msgDelegationImmutable)
}
