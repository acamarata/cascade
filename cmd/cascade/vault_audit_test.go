package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestVaultAuditRefusesRatherThanClaimingNoEvents pins the honest
// behaviour: with no recorded access log, the command must refuse. An empty
// list would be a claim that nothing has read this vault, and this build
// cannot make that claim truthfully.
func TestVaultAuditRefusesRatherThanClaimingNoEvents(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	stdout, _, err := runVault(t, deps, "", "audit")
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("vault audit = %v, want an unavailable refusal", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("vault audit printed %q; it must print no event list at all", stdout)
	}
	if !strings.Contains(err.Error(), "not recorded") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

func TestVaultAuditRejectsArguments(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "", "audit", "extra"); !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("vault audit with an argument = %v", err)
	}
}
