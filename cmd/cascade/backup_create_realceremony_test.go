//go:build !windows

// Purpose: `backup create`'s real-attestation success proof. Requires the
// FULL elevation ceremony to actually succeed, which platformElevationRefusal
// (internal/rpc/elevation_windows.go) preempts by design on Windows before
// the handler ever runs (internal/rpc/elevation_flow_windows_test.go);
// moved out of backup_create_test.go with the matching `!windows` tag
// rather than skipped, per the AGENT-BRIEF's "build tags on test files"
// rule. backup_windows_tier2_test.go is this package's Windows-side proof
// of the refusal (R-14.131).
// SPORT: cmd.cascade.backup-create/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"testing"
)

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
