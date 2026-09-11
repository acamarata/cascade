// Purpose: Restore's fail-closed proof: a genuine end-to-end round trip
//
//	(real SQLite capture -> gate -> restore -> the destination database's
//	rows compared field-by-field against the source, not "err == nil"),
//	the elevation/key-custody/domain-selection refusals, and — the
//	highest-stakes assertion this ticket makes — that a restore refused
//	by the gate leaves the destination exactly as it was found.
//
// SPORT: internal.backup.restore/ADDED (P1-E19-W4-S41-T4).

package backup

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// kvRow mirrors capture_test.go's seeded shape for direct comparison.
type kvRow struct {
	namespace, key string
	value          []byte
}

// readKVRows reads every row from db's shared kv table under namespace,
// ordered by key — the same table seedCaptureRow (capture_test.go) writes
// into and storage.Export/Import read and write, so this is a direct,
// field-by-field check of what actually landed, not a re-derivation of
// the import path's own bookkeeping.
func readKVRows(t *testing.T, db *sql.DB, namespace string) []kvRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT namespace, key, value FROM kv WHERE namespace = ? ORDER BY key`, namespace)
	if err != nil {
		// A destination that has never received a single successful
		// storage.Import call has no kv table at all yet — that is the
		// "untouched" state a refused restore must leave behind, not a
		// fixture failure, so it reads as zero rows rather than an error.
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		t.Fatalf("readKVRows: query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []kvRow
	for rows.Next() {
		var r kvRow
		if err := rows.Scan(&r.namespace, &r.key, &r.value); err != nil {
			t.Fatalf("readKVRows: scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("readKVRows: rows.Err: %v", err)
	}
	return out
}

// firstObjectKey returns one arbitrary stored objects/ key. Safe ONLY
// against a target holding a single snapshot's objects (restoreFixture's
// shape: one CreateSnapshot call, no chain) — every stored object then
// necessarily belongs to that one manifest, so which key List happens to
// return first does not matter. A target built from a CHAIN of snapshots
// does not have this property (see integrity_test.go's manifestObjectKey
// and its doc comment for why) and must not use this helper.
func firstObjectKey(t *testing.T, target *memTarget) string {
	t.Helper()
	keys, err := target.List(context.Background(), "objects/")
	if err != nil || len(keys) == 0 {
		t.Fatalf("List(objects/) = %v, %v; want at least one stored object", keys, err)
	}
	return keys[0]
}

// restoreFixture builds one real snapshot over one real SQLite domain
// (source) plus a fresh, independently-bootstrapped destination database,
// and returns everything RestoreOptions needs.
func restoreFixture(t *testing.T) (target *memTarget, pubKey ed25519.PublicKey, snapshot Manifest, destDB *sql.DB, sourceRows []kvRow) {
	t.Helper()
	ctx := context.Background()
	pub := setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target = newMemTarget()

	sourceDB := openCaptureTestDB(t)
	seedCaptureRow(t, sourceDB, "context", "alpha", []byte("first real row, restored end-to-end"))
	seedCaptureRow(t, sourceDB, "context", "beta", []byte("second real row, byte-for-byte checked"))

	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock: testkit.NewFrozenClock(time.Unix(1_700_000_200, 0)),
		Domains: map[string]Exporter{
			"context": SQLiteCapture{DB: sourceDB, Domain: storage.DomainContext, Dir: t.TempDir()},
		},
	}
	m, err := CreateSnapshot(ctx, "proof-1", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	sourceRows = readKVRows(t, sourceDB, "context")
	if len(sourceRows) != 2 {
		t.Fatalf("seeded source has %d rows under namespace context, want 2", len(sourceRows))
	}

	destDB = openCaptureTestDB(t)
	return target, pub, m, destDB, sourceRows
}

// TestRestoreRoundTrip is the real end-to-end proof the ticket demands:
// real SQLite capture (source) -> the gate -> Restore -> the destination
// database's rows, read back and compared FIELD BY FIELD against the
// source's rows — never "no error returned." A restore that silently
// produced an empty destination would pass an err==nil check; it does not
// pass this one.
func TestRestoreRoundTrip(t *testing.T) {
	target, pub, m, destDB, sourceRows := restoreFixture(t)

	report, err := Restore(context.Background(), "restore-proof-1",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub}, m.Snapshot)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if report.RowsImported != len(sourceRows) {
		t.Fatalf("report.RowsImported = %d, want %d", report.RowsImported, len(sourceRows))
	}

	destRows := readKVRows(t, destDB, "context")
	if len(destRows) != len(sourceRows) {
		t.Fatalf("destination has %d rows, want %d (source count)", len(destRows), len(sourceRows))
	}
	for i, src := range sourceRows {
		got := destRows[i]
		if got.namespace != src.namespace || got.key != src.key || string(got.value) != string(src.value) {
			t.Fatalf("row %d = {%q,%q,%q}, want {%q,%q,%q}",
				i, got.namespace, got.key, got.value, src.namespace, src.key, src.value)
		}
	}
}

// TestRestoreRefusesOnCorruptedObject_DestinationUntouched corrupts a
// REAL stored object (bit-flip, mid-stream) so the gate refuses, then
// asserts the destination database is left exactly as it was found: zero
// rows under the domain's namespace. A half-applied restore would leave
// SOME rows; this proves there are none.
func TestRestoreRefusesOnCorruptedObject_DestinationUntouched(t *testing.T) {
	target, pub, m, destDB, _ := restoreFixture(t)

	before := readKVRows(t, destDB, "context")
	if len(before) != 0 {
		t.Fatalf("destination has %d rows before restore, want 0 (fresh)", len(before))
	}

	target.corrupt(firstObjectKey(t, target))

	_, err := Restore(context.Background(), "restore-proof-1",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub}, m.Snapshot)
	if err == nil {
		t.Fatal("Restore over a corrupted object = nil error, want a refusal")
	}

	after := readKVRows(t, destDB, "context")
	if len(after) != 0 {
		t.Fatalf("destination has %d rows after a REFUSED restore, want 0 (untouched)", len(after))
	}
}

// TestRestoreRefusesOnTruncatedManifest_DestinationUntouched is the same
// proof against a different real artifact: the manifest itself, truncated
// mid-stream rather than an object.
func TestRestoreRefusesOnTruncatedManifest_DestinationUntouched(t *testing.T) {
	target, pub, m, destDB, _ := restoreFixture(t)

	target.truncate(manifestKey(m.Snapshot))

	_, err := Restore(context.Background(), "restore-proof-1",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub}, m.Snapshot)
	if err == nil {
		t.Fatal("Restore over a truncated manifest = nil error, want a refusal")
	}

	after := readKVRows(t, destDB, "context")
	if len(after) != 0 {
		t.Fatalf("destination has %d rows after a REFUSED restore, want 0 (untouched)", len(after))
	}
}

func TestRestoreElevationRefusal(t *testing.T) {
	target, pub, m, destDB, _ := restoreFixture(t)

	_, err := Restore(context.Background(), "", RestoreOptions{Target: target, DB: destDB, PubKey: pub}, m.Snapshot)
	if err != ErrRestoreElevationRequired {
		t.Fatalf("Restore(no proof) = %v, want ErrRestoreElevationRequired", err)
	}

	after := readKVRows(t, destDB, "context")
	if len(after) != 0 {
		t.Fatalf("destination has %d rows after an elevation-refused restore, want 0 (untouched)", len(after))
	}
}

func TestRestoreRequiresDestinationDB(t *testing.T) {
	target, pub, m, _, _ := restoreFixture(t)
	_, err := Restore(context.Background(), "restore-proof-1", RestoreOptions{Target: target, PubKey: pub}, m.Snapshot)
	if err == nil {
		t.Fatal("Restore(nil DB) = nil error, want a refusal")
	}
}

func TestRestoreMissingKeyMaterialRefuses(t *testing.T) {
	target, pub, m, destDB, _ := restoreFixture(t)
	t.Setenv(AgeIdentityEnvVar, "")

	_, err := Restore(context.Background(), "restore-proof-1",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub}, m.Snapshot)
	if err != ErrAgeIdentityMissing {
		t.Fatalf("Restore with no AgeIdentityEnvVar = %v, want ErrAgeIdentityMissing", err)
	}
}

// TestRestoreDomainSelectionBoundedByManifest proves `restore --domain`'s
// bound: a domain outside the verified manifest's own domain set refuses
// rather than silently restoring nothing for it.
func TestRestoreDomainSelectionBoundedByManifest(t *testing.T) {
	target, pub, m, destDB, _ := restoreFixture(t)

	_, err := Restore(context.Background(), "restore-proof-1",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub, Domains: []string{"secrets"}}, m.Snapshot)
	if err == nil {
		t.Fatal("Restore(Domains: [\"secrets\"]) over a manifest that only lists \"context\" = nil error, want a refusal")
	}
}

// TestRestoreDomainSelectionDefaultsToAllManifestDomains proves the other
// half: an empty Domains slice restores everything the manifest lists (no
// silent narrowing).
func TestRestoreDomainSelectionDefaultsToAllManifestDomains(t *testing.T) {
	target, pub, m, destDB, sourceRows := restoreFixture(t)

	report, err := Restore(context.Background(), "restore-proof-1",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub}, m.Snapshot)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(report.Domains) != 1 || report.Domains[0] != "context" {
		t.Fatalf("report.Domains = %v, want [\"context\"]", report.Domains)
	}
	if report.RowsImported != len(sourceRows) {
		t.Fatalf("report.RowsImported = %d, want %d", report.RowsImported, len(sourceRows))
	}
}
