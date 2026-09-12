// Purpose: `cascade backup create [--target] [--yes]` tests.
// Inputs: a real fs target, a real ed25519 elevation ceremony, real
// CreateSnapshot/RecordOutcome collaborators.
// Outputs: exercised RunE paths, elevation refusal, and outcome recording.
// Constraints: no target I/O begins before a proof is minted.
// SPORT: cmd.cascade.backup-create/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBackupCLICreateRequiresElevation is the acceptance criterion: without
// a valid attestation, `backup create` refuses ELEVATION_REQUIRED and never
// touches the target (no snapshot is recorded).
func TestBackupCLICreateRequiresElevation(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	addTestFSTarget(t, deps, "primary")

	_, _, err := runBackup(t, deps, "", "create", "--target", "primary", "--yes")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("create without elevation = %v, want ELEVATION_REQUIRED", err)
	}

	stdout, _, err := runBackup(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var result backupListResult
	unmarshalJSONEnvelope(t, stdout, &result)
	if len(result.Snapshots) != 0 {
		t.Fatalf("a refused create still produced %d snapshot(s)", len(result.Snapshots))
	}
}

// TestBackupCLICreateNoInputMissingYes proves CASCADE_NO_INPUT=1 with no
// --yes exits 1 with a structured error, never a prompt, for the create
// verb specifically (backup_elevation_test.go proves the shared authorizer
// logic directly; this proves runBackupCreate actually reaches it before
// any target I/O).
func TestBackupCLICreateNoInputMissingYes(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, map[string]string{"CASCADE_NO_INPUT": "1"})
	deps := testBackupDeps(t, newBackupAuthorizer(elevateDeps))
	addTestFSTarget(t, deps, "primary")

	_, stderr, err := runBackup(t, deps, "", "create", "--target", "primary")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("create with CASCADE_NO_INPUT=1 and no --yes = %v, want KindElevationRequired", err)
	}
	if !strings.Contains(err.Error(), "CASCADE_NO_INPUT") {
		t.Fatalf("error %v does not name CASCADE_NO_INPUT", err)
	}
	if strings.Contains(stderr, "Continue? [y/N]") {
		t.Fatal("a confirmation prompt was written under CASCADE_NO_INPUT=1")
	}
}

// TestBackupCLICreateSucceedsWithRealAttestation is the Art.2 real-stack
// wiring proof: a genuine ed25519 elevation ceremony over a real fs target
// produces a real signed snapshot manifest, then a mutation removing the
// elevation gate call (proven in backup_elevation_test.go's RED case) shows
// the refusal path is not a no-op -- together, both directions prove
// runBackupCreate really calls deps.Authorize then deps.Create, not a
// bypass in either direction.
func TestBackupCLICreateSucceedsWithRealAttestation(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	deps := testBackupDeps(t, newBackupAuthorizer(elevateDeps))
	addTestFSTarget(t, deps, "primary")

	if _, _, err := runBackup(t, deps, "", "create", "--target", "primary", "--yes"); err != nil {
		t.Fatalf("create with a real attestation: %v", err)
	}

	listOut, _, err := runBackup(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	var result backupListResult
	unmarshalJSONEnvelope(t, listOut, &result)
	if len(result.Snapshots) != 1 || result.Snapshots[0].Outcome != "success" {
		t.Fatalf("list after a real create = %+v, want exactly one successful snapshot", result.Snapshots)
	}
}

// TestBackupCLICreateUnknownTarget proves selecting a target name that was
// never registered is a typed not-found refusal, before any elevation
// attempt.
func TestBackupCLICreateUnknownTarget(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	if _, _, err := runBackup(t, deps, "", "create", "--target", "absent", "--yes"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("create --target absent = %v, want KindNotFound", err)
	}
}
