package economics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	sqlitestore "github.com/acamarata/cascade/providers/sqlite"
)

func TestModeSetLoosens(t *testing.T) {
	for _, raw := range []string{`{"mode":"incident"}`, `{"mode":""}`, `{"mode":"bogus"}`, `not JSON`, `{}`, `null`, `{"mode":3}`} {
		if !ModeSetLoosens(json.RawMessage(raw)) {
			t.Errorf("ModeSetLoosens(%s) = false, want true", raw)
		}
	}
	count := 0
	for _, mode := range Modes() {
		if mode == ModeIncident {
			continue
		}
		count++
		raw, _ := json.Marshal(map[string]string{"mode": string(mode)})
		if ModeSetLoosens(raw) {
			t.Errorf("ModeSetLoosens(%s) = true, want false", raw)
		}
	}
	if count != 7 {
		t.Fatalf("tested %d non-incident modes, want 7", count)
	}
}

// testClock is a minimal economics.Clock for tests that do not need a
// mutable frozen clock.
type testClock struct{ t time.Time }

func (c testClock) Now() time.Time { return c.t }

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/economics.db?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := testClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return db
}

func TestApplyMigrationSchemaIdempotent(t *testing.T) {
	db := newTestDB(t)
	clock := testClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema: %v", err)
	}
}

func TestApplyMigrationSchemaRequiresDBAndClock(t *testing.T) {
	if err := ApplyMigrationSchema(context.Background(), nil, migrate.SQLiteEmitter{}, testClock{}, "", ""); err == nil {
		t.Error("want error for nil db")
	}
	db := newTestDB(t)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("want error for nil clock")
	}
}

const modeChildEnv = "CASCADE_MODE_EFFECT_CHILD"

func modeAuditFixture(t *testing.T, dir string) (*SchedulerModeStore, *audit.EffectLog, *audit.Log) {
	t.Helper()
	ctx := context.Background()
	clock := testClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "mode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	backing, err := sqlitestore.Open(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backing.Close() })
	log := audit.New(backing, clock, nil)
	effects, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatal(err)
	}
	return NewSchedulerModeStore(db, clock), effects, log
}

func modeCaller() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{
		Principal: &principal.Principal{ID: "session-mode-test", Kind: principal.KindSession},
	})
}

// modeCrashWriter is a test-only crash seam at the write/confirm boundary.
type modeCrashWriter struct{ audit.EffectWriter }

func (w modeCrashWriter) ConfirmEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	os.Exit(3)
	return audit.Record{}, nil
}

// modeOrderedWriter checks the handler's actual write boundary.
type modeOrderedWriter struct {
	audit.EffectWriter
	t     *testing.T
	store *SchedulerModeStore
}

func (w modeOrderedWriter) BeginEffect(ctx context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	w.t.Helper()
	if _, found, err := w.store.selectModeRow(ctx, "p"); err != nil || found {
		w.t.Fatalf("mode written before intent: %v, %v", found, err)
	}
	return w.EffectWriter.BeginEffect(ctx, req)
}

func (w modeOrderedWriter) ConfirmEffect(ctx context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	w.t.Helper()
	if row, found, err := w.store.selectModeRow(ctx, "p"); err != nil || !found || row.mode != "incident" {
		w.t.Fatalf("confirm before write: %+v, %v, %v", row, found, err)
	}
	return w.EffectWriter.ConfirmEffect(ctx, h, outcome)
}

func TestModeSetHandlerIncidentAuditPair(t *testing.T) {
	if dir := os.Getenv(modeChildEnv); dir != "" {
		store, effects, _ := modeAuditFixture(t, dir)
		_, err := ModeSetHandler(store, nil, store.clock, modeCrashWriter{effects})(modeCaller(), json.RawMessage(`{"project_id":"p","mode":"incident"}`))
		t.Fatalf("crash seam not reached: %v", err)
	}
	t.Run("pair", testModeAuditPair)
	t.Run("crash", testModeAuditCrash)
	t.Run("refuse", testModeAuditRefusal)
}

func testModeAuditPair(t *testing.T) {
	store, effects, log := modeAuditFixture(t, t.TempDir())
	ctx := modeCaller()
	handler := ModeSetHandler(store, nil, store.clock, modeOrderedWriter{effects, t, store})
	raw := json.RawMessage(`{"project_id":"p","mode":"incident"}`)
	if _, err := handler(ctx, raw); err != nil {
		t.Fatal(err)
	}
	page, err := log.Query(ctx, audit.Filter{})
	if err != nil || len(page.Records) != 2 {
		t.Fatalf("audit pair = %+v, %v", page, err)
	}
	for i, phase := range []audit.EffectPhase{audit.EffectIntent, audit.EffectConfirmed} {
		r := page.Records[i]
		if r.Actor != "session-mode-test" || r.Action != MethodFleetModeSet || r.EffectPhase != string(phase) || r.ParamsHash != rpc.ParamsHash(raw) {
			t.Fatalf("record %d = %+v", i, r)
		}
	}
	if page.Records[0].EffectKey != page.Records[1].EffectKey {
		t.Fatal("pair keys differ")
	}
	handler = ModeSetHandler(store, nil, store.clock, effects)
	if _, err := handler(ctx, raw); err == nil {
		t.Fatal("duplicate incident effect accepted")
	} else {
		assertModeEffectExists(t, err)
	}
	if _, err := handler(ctx, json.RawMessage(`{"project_id":"p","mode":"crunch"}`)); err != nil {
		t.Fatal(err)
	}
	page, err = log.Query(ctx, audit.Filter{})
	if err != nil || len(page.Records) != 2 {
		t.Fatalf("crunch or duplicate wrote audit pair: %+v, %v", page, err)
	}
}

func assertModeEffectExists(t *testing.T, err error) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == audit.ErrEffectExists && e.Error() == audit.ErrEffectExists.Error() {
			return
		}
	}
	t.Fatalf("missing ErrEffectExists identity and message: %v", err)
}

func testModeAuditCrash(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestModeSetHandlerIncidentAuditPair$", "-test.count=1")
	cmd.Env = append(os.Environ(), modeChildEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("child exit = %v, want 3: %s", err, out)
	}
	store, effects, log := modeAuditFixture(t, dir)
	pending, err := effects.PendingEffects(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if phase, found, err := effects.EffectState(ctx, pending[0].Key); err != nil || !found || phase != audit.EffectIntent {
		t.Fatalf("crash state = %s, %v, %v", phase, found, err)
	}
	row, found, err := store.selectModeRow(ctx, "p")
	if err != nil || !found || row.mode != "incident" || row.incidentRenewals != 1 {
		t.Fatalf("effect did not run exactly once: %+v, %v, %v", row, found, err)
	}
	if _, err := effects.MarkUnknownOutcome(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	_, err = ModeSetHandler(store, nil, store.clock, effects)(modeCaller(), json.RawMessage(`{"project_id":"p","mode":"incident"}`))
	assertModeEffectExists(t, err)
	page, err := log.Query(ctx, audit.Filter{})
	if err != nil || len(page.Records) != 2 || page.Records[1].EffectPhase != string(audit.EffectUnknown) {
		t.Fatalf("recovered records = %+v, %v", page, err)
	}
	row, _, err = store.selectModeRow(ctx, "p")
	if err != nil || row.incidentRenewals != 1 {
		t.Fatalf("retry repeated effect: %+v, %v", row, err)
	}
}

func testModeAuditRefusal(t *testing.T) {
	store, effects, _ := modeAuditFixture(t, t.TempDir())
	raw := json.RawMessage(`{"project_id":"p","mode":"incident"}`)
	if _, err := ModeSetHandler(store, nil, store.clock)(modeCaller(), raw); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("missing audit = %v", err)
	}
	if _, err := ModeSetHandler(store, nil, store.clock, effects)(context.Background(), raw); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("missing caller = %v", err)
	}
	if _, found, err := store.selectModeRow(context.Background(), "p"); err != nil || found {
		t.Fatalf("refused request wrote mode: %v, %v", found, err)
	}
}
