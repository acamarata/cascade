//go:build integration

// Purpose: S-42.T5's acceptance drill -- lose-the-laptop restore under a
// concurrent write (§D-15), a can-fail proof, and an honest ceremony-only
// (no env vars) run confirming the tracked defect: manifest.go/integrity.go
// resolve key material from the environment only, never the S-42.T6
// ceremony's vault. Inputs: a real targets.FSTarget (Art.2's CI-runnable
// fallback; no owner S3/B2 credential exists here) and real SQLite
// domains via this package's own capture/restore test helpers. Outputs:
// none beyond pass/fail; no production code added. Constraints:
// cmd/cascade is `package main`, unimportable here, so every path runs
// through the same internal/backup functions the CLI calls. §D-15's
// write is a deterministic open, uncommitted transaction (Art.7.3), no
// goroutine/sleep, mirroring TestCaptureExcludesUncommittedWrite.
// SPORT: internal.backup.drill/ADD (P1-E19-W4-S42-T5).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

var drillDomains = []struct {
	id  storage.DomainID
	key string
	val string
}{
	{storage.DomainContext, "ctx-1", "owner context row"},
	{storage.DomainMemory, "mem-1", "owner memory row"},
	{storage.DomainSecrets, "vault-1", "non-secret vault test entry"},
	{storage.DomainConfig, "phase-1", "phase-state entry"},
	{storage.DomainSessions, "turn-1", "conversation turn"},
}

const drillContextNS = string(storage.DomainContext)

func drillMust(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// drillAllowGate always authorizes -- the attestation flow is proven elsewhere.
type drillAllowGate struct{}

func (drillAllowGate) Authorize(context.Context, string) error { return nil }

// drillVaultStore adapts *secrets.Broker to VaultStore (package main's adapter is not importable here).
type drillVaultStore struct{ broker *secrets.Broker }

func (v drillVaultStore) Exists(ctx context.Context, name string) (bool, error) {
	return v.broker.Exists(ctx, name)
}

func (v drillVaultStore) Get(ctx context.Context, name string) ([]byte, error) {
	return v.broker.Get(ctx, name)
}

func (v drillVaultStore) Set(ctx context.Context, name string, value []byte) error {
	_, err := v.broker.Set(ctx, name, value, secrets.SetUpdate)
	return err
}

// drillFailingRunner: belt-and-suspenders for the repo's keychain gate.
func drillFailingRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "drill: no external command runs in this test")
}

func newDrillBroker(t *testing.T, dir string) *secrets.Broker {
	t.Helper()
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-backup-drill", Dir: dir, ForceFileVault: true, Runner: drillFailingRunner,
	})
	drillMust(t, err, "SelectCustody")
	broker, err := secrets.NewBroker(custody, drillAllowGate{})
	drillMust(t, err, "NewBroker")
	return broker
}

func newDrillEscrow(t *testing.T) EscrowChecker {
	t.Helper()
	log := audit.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1_700_000_500, 0)), nil)
	drillMust(t, RecordKeyEscrowed(context.Background(), log, "backup"), "RecordKeyEscrowed")
	return AuditEscrowChecker{Reader: log}
}

func seedDrillBaseline(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, d := range drillDomains {
		seedCaptureRow(t, db, string(d.id), d.key, []byte(d.val))
	}
}

func drillCaptureDomains(db *sql.DB, dir string) map[string]Exporter {
	out := make(map[string]Exporter, len(drillDomains))
	for _, d := range drillDomains {
		out[string(d.id)] = SQLiteCapture{DB: db, Domain: d.id, Dir: dir}
	}
	return out
}

// assertDrillDoctor: every seeded row round-tripped, in-flight row absent.
func assertDrillDoctor(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, d := range drillDomains {
		rows := readKVRows(t, db, string(d.id))
		found := false
		for _, r := range rows {
			if r.key == d.key {
				found = true
				if string(r.value) != d.val {
					t.Fatalf("restored %s/%s = %q, want %q", d.id, d.key, r.value, d.val)
				}
			}
		}
		if !found {
			t.Fatalf("restored state is missing %s/%s", d.id, d.key)
		}
	}
	for _, r := range readKVRows(t, db, drillContextNS) {
		if r.key == "ctx-inflight" {
			t.Fatal("restored state contains the uncommitted in-flight row -- torn snapshot reached restore")
		}
	}
}

func corruptTargetObject(t *testing.T, target Target, key string) {
	t.Helper()
	ctx := context.Background()
	rc, err := target.Get(ctx, key)
	if err != nil {
		t.Fatalf("corruptTargetObject: Get(%q): %v", key, err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || len(data) == 0 {
		t.Fatalf("corruptTargetObject: read %q: %v (len=%d)", key, err, len(data))
	}
	data[0] ^= 0xFF
	if err := target.Put(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatalf("corruptTargetObject: Put(%q): %v", key, err)
	}
}

func firstTargetObjectKey(t *testing.T, target Target) string {
	t.Helper()
	keys, err := target.List(context.Background(), "objects/")
	if err != nil || len(keys) == 0 {
		t.Fatalf("List(objects/) = %v, %v; want at least one object", keys, err)
	}
	return keys[0]
}

func drillOneDomainSnapshot(t *testing.T, target Target, recipient string, seed int64) (Manifest, error) {
	t.Helper()
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, drillContextNS, "ctx-1", []byte("owner context row"))
	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(seed, 0)),
		Domains: map[string]Exporter{drillContextNS: SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()}},
	}
	return CreateSnapshot(context.Background(), "drill-create-proof", deps, nil)
}

// drillCreateUnderConcurrentWrite: snapshot taken during an uncommitted write on drillContextNS (§D-15).
func drillCreateUnderConcurrentWrite(t *testing.T, target Target, recipient string) Manifest {
	t.Helper()
	ctx := context.Background()
	sourceDB := openCaptureTestDB(t)
	seedDrillBaseline(t, sourceDB)
	tx, err := sourceDB.Begin()
	drillMust(t, err, "Begin")
	_, err = tx.ExecContext(ctx, `INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)`,
		drillContextNS, "ctx-inflight", []byte("uncommitted concurrent write"))
	drillMust(t, err, "tx.ExecContext")
	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(1_700_000_500, 0)),
		Domains: drillCaptureDomains(sourceDB, t.TempDir()),
		Escrow:  newDrillEscrow(t),
	}
	manifest, err := CreateSnapshot(ctx, "drill-create-proof", deps, nil)
	drillMust(t, err, "CreateSnapshot under a concurrent uncommitted write")
	drillMust(t, tx.Rollback(), "Rollback the in-flight write")
	return manifest
}

// assertDrillMachineLoss: a fresh, zero-row database is all a restoring machine may start from.
func assertDrillMachineLoss(t *testing.T, target Target) {
	t.Helper()
	if keys, err := target.List(context.Background(), "manifests/"); err != nil || len(keys) == 0 {
		t.Fatalf("target lost its manifests across the simulated machine loss: %v, %v", keys, err)
	}
	fresh := openCaptureTestDB(t)
	for _, d := range drillDomains {
		if rows := readKVRows(t, fresh, string(d.id)); len(rows) != 0 {
			t.Fatalf("a fresh install already has %d rows under %q, want 0", len(rows), d.id)
		}
	}
}

// TestBackupDrillLoseTheLaptop: concurrent-write snapshot, source destroyed, fresh install restores byte-for-byte.
func TestBackupDrillLoseTheLaptop(t *testing.T) {
	ctx := context.Background()
	pub := setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target, err := targets.NewFSTarget(t.TempDir())
	drillMust(t, err, "NewFSTarget")
	manifest := drillCreateUnderConcurrentWrite(t, target, recipient)
	_, gate, err := VerifyIntegrity(ctx, GateOptions{Target: target, PubKey: pub}, manifest.Snapshot)
	drillMust(t, err, "VerifyIntegrity (manifest signature, zero corrupt chunks)")
	if gate.ObjectsVerified == 0 || len(manifest.Domains) != len(drillDomains) {
		t.Fatalf("manifest = %d objects, %v domains; want >0 objects, all %d domains", gate.ObjectsVerified, manifest.Domains, len(drillDomains))
	}
	assertDrillMachineLoss(t, target)
	destDB := openCaptureTestDB(t)
	_, err = Restore(ctx, "drill-restore-proof", RestoreOptions{Target: target, DB: destDB, PubKey: pub}, manifest.Snapshot)
	drillMust(t, err, "Restore on the fresh install")
	assertDrillDoctor(t, destDB)
}

// TestBackupDrillCanFail: a corrupted object and a withheld key each refuse closed, destination untouched.
func TestBackupDrillCanFail(t *testing.T) {
	ctx := context.Background()
	pub := setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target, err := targets.NewFSTarget(t.TempDir())
	drillMust(t, err, "NewFSTarget")
	manifest, err := drillOneDomainSnapshot(t, target, recipient, 1_700_000_600)
	drillMust(t, err, "CreateSnapshot")
	corruptTargetObject(t, target, firstTargetObjectKey(t, target))
	destDB := openCaptureTestDB(t)
	if _, err := Restore(ctx, "drill-restore-proof", RestoreOptions{Target: target, DB: destDB, PubKey: pub}, manifest.Snapshot); err == nil {
		t.Fatal("Restore over a corrupted object = nil error, want a refusal")
	}
	if rows := readKVRows(t, destDB, drillContextNS); len(rows) != 0 {
		t.Fatalf("destination has %d rows after a REFUSED restore, want 0 (untouched)", len(rows))
	}
	t.Setenv(AgeIdentityEnvVar, "")
	if _, err := Restore(ctx, "drill-restore-proof", RestoreOptions{Target: target, DB: destDB, PubKey: pub}, manifest.Snapshot); err != ErrAgeIdentityMissing {
		t.Fatalf("Restore with the recovery key withheld = %v, want ErrAgeIdentityMissing", err)
	}
}
