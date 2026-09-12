// Purpose: `backup target add|list|remove` tests over S-42.T1's registry.
// Inputs: fs/s3/rclone kinds, cron/domain flags, and a secret-shaped literal.
// Outputs: exercised RunE paths driven through the real cobra command tree.
// Constraints: creds are never accepted; a secret-looking literal refuses.
// SPORT: cmd.cascade.backup-target/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"strings"
	"testing"
)

// TestBackupCLITargetAddListRemove drives the full fs-target lifecycle
// through the real cobra tree: add, list (proving the persisted fields
// round-trip), and remove (proving it is actually gone from list).
func TestBackupCLITargetAddListRemove(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	root := t.TempDir()

	if _, _, err := runBackup(t, deps, "", "target", "add", "primary", "fs", root,
		"--cron", "0 3 * * *", "--domain", "config"); err != nil {
		t.Fatalf("target add: %v", err)
	}

	stdout, _, err := runBackup(t, deps, "", "target", "list", "--json")
	if err != nil {
		t.Fatalf("target list: %v", err)
	}
	var result struct {
		Targets []backupTargetView `json:"targets"`
	}
	unmarshalJSONEnvelope(t, stdout, &result)
	if len(result.Targets) != 1 || result.Targets[0].Name != "primary" ||
		result.Targets[0].Kind != "fs" || result.Targets[0].LocationRef != root {
		t.Fatalf("target list = %+v, want one fs target named primary at %q", result.Targets, root)
	}

	if _, _, err := runBackup(t, deps, "", "target", "remove", "primary"); err != nil {
		t.Fatalf("target remove: %v", err)
	}
	stdout, _, err = runBackup(t, deps, "", "target", "list", "--json")
	if err != nil {
		t.Fatalf("target list after remove: %v", err)
	}
	result.Targets = nil
	unmarshalJSONEnvelope(t, stdout, &result)
	if len(result.Targets) != 0 {
		t.Fatalf("target list after remove = %+v, want zero targets", result.Targets)
	}
}

// TestBackupCLITargetAddUnknownKind proves an unrecognized driver kind is a
// typed refusal, not a silently-accepted record.
func TestBackupCLITargetAddUnknownKind(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	_, _, err := runBackup(t, deps, "", "target", "add", "primary", "ftp", "somewhere",
		"--cron", "0 3 * * *", "--domain", "config")
	if err == nil || !strings.Contains(err.Error(), "unknown target kind") {
		t.Fatalf("target add with kind=ftp = %v, want an unknown-kind refusal", err)
	}
}

// TestBackupCLITargetAddRefusesSecretLiteral proves a credential-shaped
// location-ref value is refused at registration time (H/S-15.T3), never
// silently persisted. The literal is split so no contiguous match reaches
// source control, per the AGENT BRIEF's push-protection note; the
// concatenated runtime value is unchanged.
func TestBackupCLITargetAddRefusesSecretLiteral(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	secretShaped := "AKIA" + "7YQ2XPLM4RZV6WTB"
	_, _, err := runBackup(t, deps, "", "target", "add", "primary", "s3", secretShaped,
		"--cron", "0 3 * * *", "--domain", "config")
	if err == nil || !strings.Contains(err.Error(), "literal credential") {
		t.Fatalf("target add with a secret-shaped location-ref = %v, want the literal-credential refusal", err)
	}
	stdout, _, err := runBackup(t, deps, "", "target", "list", "--json")
	if err != nil {
		t.Fatalf("target list: %v", err)
	}
	if strings.Contains(stdout, secretShaped) {
		t.Fatal("a refused target add still leaked the secret-shaped literal into the registry")
	}
}

// TestBackupCLITargetAddMissingRequiredFlags proves --cron and --domain are
// truly required, not merely documented as required.
func TestBackupCLITargetAddMissingRequiredFlags(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	if _, _, err := runBackup(t, deps, "", "target", "add", "primary", "fs", t.TempDir()); err == nil {
		t.Fatal("target add with no --cron/--domain succeeded, want a required-flag refusal")
	}
}

// TestBackupCLITargetRemoveAbsent proves removing a name that was never
// added is a typed not-found error, never a silent no-op success.
func TestBackupCLITargetRemoveAbsent(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	if _, _, err := runBackup(t, deps, "", "target", "remove", "absent"); err == nil {
		t.Fatal("removing an absent target succeeded, want a not-found refusal")
	}
}
