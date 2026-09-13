//go:build !windows

// Purpose: the three `cascade backup verify` tests, plus their fixture
// helpers, that need a real signed snapshot from the full elevation
// ceremony. That ceremony is architecturally unreachable on Windows --
// platformElevationRefusal (internal/rpc/elevation_windows.go) preempts it
// before the handler ever runs (internal/rpc/elevation_flow_windows_test.go)
// -- so this file carries the same `!windows` tag as the POSIX attestation
// flow itself (elevation_unix.go), per the AGENT-BRIEF's "build tags on
// test files" rule. backup_windows_tier2_test.go is this package's
// Windows-side proof of the refusal (R-14.131).
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
	"github.com/acamarata/cascade/pkg/cascade"
)

// createRealVerifiableSnapshot registers a real fs target and creates one
// real, well-formed, authorized snapshot on it via the exact `backup
// create` cobra path backup_create_realceremony_test.go already proves
// real -- this file adds no second way of producing a snapshot. Returns
// the target record for corrupting/verifying afterward.
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
