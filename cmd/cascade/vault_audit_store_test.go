// Purpose: unit coverage for vaultQuarantineStore's fail-closed branches,
//
//	which shipped with no direct test of their own: an injected provider
//	takes priority, a nil Paths refuses, an unresolvable data directory
//	refuses, and a real Paths opens a real ledger.
//
// SPORT: cmd.cascade.vault-audit/TEST (vault audit composition root).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestVaultQuarantineStore_PrefersInjectedProvider(t *testing.T) {
	called := false
	deps := vaultDeps{Quarantine: func() (*secrets.QuarantineStore, error) {
		called = true
		return nil, nil
	}}
	if _, err := vaultQuarantineStore(deps); err != nil {
		t.Fatalf("vaultQuarantineStore: %v", err)
	}
	if !called {
		t.Error("vaultQuarantineStore did not call the injected Quarantine provider")
	}
}

func TestVaultQuarantineStore_NilPathsRefuses(t *testing.T) {
	_, err := vaultQuarantineStore(vaultDeps{})
	if !isCLIKind(err, cascade.KindInternal) {
		t.Fatalf("vaultQuarantineStore(no provider, nil Paths) = %v, want KindInternal", err)
	}
}

// TestVaultQuarantineStore_UnresolvableDataDirRefuses uses
// emptyDataDirPaths rather than fakeDaemonPaths{root:""}, because
// fakeDaemonPaths.DataDir() always joins root with "data" and so is
// never actually empty -- the unresolvable branch needs a provider whose
// DataDir() is genuinely "".
func TestVaultQuarantineStore_UnresolvableDataDirRefuses(t *testing.T) {
	_, err := vaultQuarantineStore(vaultDeps{Paths: emptyDataDirPaths{}})
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("vaultQuarantineStore(empty DataDir) = %v, want KindUnavailable", err)
	}
}

// emptyDataDirPaths (defined in node_serve_test.go) is a
// runtime.PathProvider whose DataDir() is genuinely empty, distinct from
// fakeDaemonPaths (whose DataDir() always joins root with "data" and so
// is never empty) -- reused here rather than redeclared.

func TestVaultQuarantineStore_RealPathsOpensRealLedger(t *testing.T) {
	deps := vaultDeps{Paths: fakeDaemonPaths{root: t.TempDir()}}
	store, err := vaultQuarantineStore(deps)
	if err != nil {
		t.Fatalf("vaultQuarantineStore: %v", err)
	}
	if store == nil {
		t.Error("vaultQuarantineStore: store is nil")
	}
}
