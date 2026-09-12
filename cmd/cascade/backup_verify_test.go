// Purpose: `cascade backup verify [--target] [--json]` tests: pass exits 0
// with a valid VerificationReport, a real corrupted object exits nonzero
// with a structured error, and an unregistered target exits with a
// target-not-found error -- driven through the real cobra command tree,
// never a mock of runBackupVerify's own logic.
// SPORT: cmd.cascade.backup-verify/ADD (tests) (P1-E19-W4-S42-T4).
package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// withVerifyDeps wires deps.Verify/NewAttentionSink onto a testBackupDeps
// base exactly as productionBackupDeps assigns them.
func withVerifyDeps(deps backupDeps) backupDeps {
	clock := deps.Clock
	deps.Verify = backup.RunVerification
	deps.NewAttentionSink = func(store provider.Store) backup.AttentionSink {
		return supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0)
	}
	return deps
}

// createRealVerifiableSnapshot registers a real fs target and creates one
// real, well-formed, authorized snapshot on it via the exact `backup
// create` cobra path TestBackupCLICreateSucceedsWithRealAttestation
// already proves real (backup_create_test.go) -- this file adds no second
// way of producing a snapshot. Returns the target record for
// corrupting/verifying afterward.
func createRealVerifiableSnapshot(t *testing.T, deps backupDeps, name string) backup.TargetRecord {
	t.Helper()
	addTestFSTarget(t, deps, name)
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	createDeps := deps
	createDeps.Authorize = newBackupAuthorizer(elevateDeps)
	if _, _, err := runBackup(t, createDeps, "", "create", "--target", name, "--yes"); err != nil {
		t.Fatalf("create --target %s --yes: %v", name, err)
	}
	rt, err := deps.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rt.Close()
	record, err := backup.GetTarget(context.Background(), rt.Store, backupRegistryNamespace, name)
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	return record
}

// firstStoredObjectPath walks record's real fs root and returns the first
// regular file found under objects/ -- the real, on-disk content
// RunVerification's integrity gate re-decrypts and hash-checks. Fails the
// test if none exists, so a corruption test can never be silently vacuous.
func firstStoredObjectPath(t *testing.T, record backup.TargetRecord) string {
	t.Helper()
	objectsDir := filepath.Join(record.FSRoot, "objects")
	var found string
	err := filepath.WalkDir(objectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return err
		}
		found = path
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", objectsDir, err)
	}
	if found == "" {
		t.Fatalf("no stored object found under %s -- fixture produced an empty snapshot", objectsDir)
	}
	return found
}

// TestBackupCLIVerifyGreenPath proves `backup verify --json` exits 0 with
// a valid VerificationReport over a real, well-formed snapshot.
func TestBackupCLIVerifyGreenPath(t *testing.T) {
	deps := withVerifyDeps(testBackupDeps(t, backupDenyGate{}.authorize))
	createRealVerifiableSnapshot(t, deps, "primary")

	stdout, _, err := runBackup(t, deps, "", "verify", "--target", "primary", "--json")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	var report backup.VerificationReport
	unmarshalJSONEnvelope(t, stdout, &report)
	if !report.Verified {
		t.Fatalf("report.Verified = false, want true: %+v", report)
	}
}

// TestBackupCLIVerifyDetectsRealCorruption proves `backup verify` fails a
// real corrupted object -- never against a digest the artifact itself
// supplied -- with a structured, typed error.
func TestBackupCLIVerifyDetectsRealCorruption(t *testing.T) {
	deps := withVerifyDeps(testBackupDeps(t, backupDenyGate{}.authorize))
	record := createRealVerifiableSnapshot(t, deps, "primary")
	objectPath := firstStoredObjectPath(t, record)

	original, rerr := os.ReadFile(objectPath)
	if rerr != nil {
		t.Fatalf("os.ReadFile: %v", rerr)
	}
	corrupted := append([]byte{}, original...)
	corrupted[0] ^= 0xFF
	if werr := os.WriteFile(objectPath, corrupted, 0o600); werr != nil {
		t.Fatalf("os.WriteFile: %v", werr)
	}

	_, _, err := runBackup(t, deps, "", "verify", "--target", "primary")
	if err == nil {
		t.Fatal("verify over a corrupted object = nil error, want a failure")
	}
	if _, ok := cascade.KindOf(err); !ok {
		t.Fatalf("verify error %v is not a typed cascade error", err)
	}
}

// TestBackupCLIVerifyUnregisteredTarget proves naming a target that was
// never registered fails with KindNotFound -- selectBackupRecord's own
// real refusal, not a synthesized one.
func TestBackupCLIVerifyUnregisteredTarget(t *testing.T) {
	deps := withVerifyDeps(testBackupDeps(t, backupDenyGate{}.authorize))
	_, _, err := runBackup(t, deps, "", "verify", "--target", "does-not-exist")
	if !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("verify --target does-not-exist = %v, want KindNotFound", err)
	}
}

// TestBackupCLIVerifyNeverAsksElevation proves verify never authorizes
// through deps.Authorize -- it is read-only per this ticket's contract.
func TestBackupCLIVerifyNeverAsksElevation(t *testing.T) {
	calls := 0
	deps := withVerifyDeps(testBackupDeps(t, func(context.Context, *cobra.Command, string, []byte, bool) (backup.ElevationProof, error) {
		calls++
		return "", backup.ErrElevationRequired
	}))
	createRealVerifiableSnapshot(t, deps, "primary")
	if _, _, err := runBackup(t, deps, "", "verify", "--target", "primary", "--json"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if calls != 0 {
		t.Fatalf("verify called Authorize %d time(s), want 0", calls)
	}
}
