// Purpose: unit coverage for productionVaultDeps' field assembly and its
//
//	NewCustody/Quarantine closures, which shipped with no direct test of
//	their own (other test files build vaultDeps by hand instead).
//	CASCADE_HOME is redirected to t.TempDir() so this never touches the
//	operator's real keychain or data directory.
//
// SPORT: cmd.cascade.vault/TEST (vault composition root).
package main

import "testing"

func TestProductionVaultDeps_AssemblesEveryField(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	deps := productionVaultDeps()
	switch {
	case deps.Paths == nil:
		t.Error("productionVaultDeps: Paths is nil")
	case deps.Getenv == nil:
		t.Error("productionVaultDeps: Getenv is nil")
	case deps.NewCustody == nil:
		t.Error("productionVaultDeps: NewCustody is nil")
	case deps.Gate == nil:
		t.Error("productionVaultDeps: Gate is nil")
	case deps.ReadFile == nil:
		t.Error("productionVaultDeps: ReadFile is nil")
	case deps.Quarantine == nil:
		t.Error("productionVaultDeps: Quarantine is nil")
	case deps.StdinIsPiped == nil:
		t.Error("productionVaultDeps: StdinIsPiped is nil")
	}
}

// TestProductionVaultDeps_QuarantineOpensRealStore drives the Quarantine
// closure over a real, redirected data directory, proving it actually
// resolves paths.DataDir() and opens a real ledger rather than being
// unreachable wiring.
func TestProductionVaultDeps_QuarantineOpensRealStore(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	deps := productionVaultDeps()
	store, err := deps.Quarantine()
	if err != nil {
		t.Fatalf("Quarantine(): %v", err)
	}
	if store == nil {
		t.Error("Quarantine(): store is nil")
	}
}
