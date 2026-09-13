// Purpose: `cascade backup verify [--target] [--json]` tests that do not
// require a real snapshot. The three tests that do (GreenPath,
// DetectsRealCorruption, NeverAsksElevation) and createRealVerifiableSnapshot
// live in backup_verify_realceremony_test.go (`!windows`): Windows's
// tier-2 platform refusal (internal/rpc/elevation_windows.go) preempts the
// real elevation ceremony that fixture needs before it can ever run.
// SPORT: cmd.cascade.backup-verify/ADD (tests) (P1-E19-W4-S42-T4).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// withVerifyDeps wires deps.Verify/NewAttentionSink onto a testBackupDeps
// base exactly as productionBackupDeps assigns them. Shared with
// backup_verify_realceremony_test.go.
func withVerifyDeps(deps backupDeps) backupDeps {
	clock := deps.Clock
	deps.Verify = backup.RunVerification
	deps.NewAttentionSink = func(store provider.Store) backup.AttentionSink {
		return supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0)
	}
	return deps
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
