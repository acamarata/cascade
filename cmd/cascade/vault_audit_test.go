// Purpose: tests for `cascade vault audit`. Every one drives the real
//
//	vault command tree against a temp-dir file vault, and every one
//	asserts the surface withholds values.
//
// SPORT: cmd/cascade/vault (ADD - audit tests).

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
)

// auditDeps builds vault deps whose custody and quarantine ledger both
// live under t.TempDir(), and seeds the given entries.
func auditDeps(t *testing.T, entries map[string]string) vaultDeps {
	t.Helper()
	deps := testVaultDeps(t, okGate{}, nil)
	custody, err := deps.NewCustody()
	if err != nil {
		t.Fatalf("NewCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, okGate{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	for name, value := range entries {
		if _, serr := broker.Set(context.Background(), name, []byte(value), secrets.SetUpdate); serr != nil {
			t.Fatalf("seeding %s: %v", name, serr)
		}
	}
	return deps
}

// TestVaultAuditReportsNamesAndNeverValues is the core guarantee: the
// entry appears by name and its value appears nowhere in the output.
func TestVaultAuditReportsNamesAndNeverValues(t *testing.T) {
	const value = "correct-horse-battery-staple"
	deps := auditDeps(t, map[string]string{"TEAM_PASSPHRASE": value})
	stdout, _, err := runVault(t, deps, "", "audit")
	if err != nil {
		t.Fatalf("vault audit: %v", err)
	}
	if !strings.Contains(stdout, "TEAM_PASSPHRASE") {
		t.Fatalf("the report omits the stored name:\n%s", stdout)
	}
	if strings.Contains(stdout, value) {
		t.Fatalf("the audit surface printed a stored value:\n%s", stdout)
	}
	if !strings.Contains(stdout, "quarantined detections") {
		t.Fatalf("the report omits the quarantine depth:\n%s", stdout)
	}
}

// TestVaultAuditIsIdempotent pins the read-only contract: two runs over
// an unchanged vault produce the same entry list.
func TestVaultAuditIsIdempotent(t *testing.T) {
	deps := auditDeps(t, map[string]string{"A_KEY": "aaaaaaaaaa", "B_KEY": "bbbbbbbbbb"})
	first, _, err := runVault(t, deps, "", "audit")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, _, err := runVault(t, deps, "", "audit")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first != second {
		t.Fatalf("two audit runs differ:\n%s\n---\n%s", first, second)
	}
}

// TestVaultAuditJSONCarriesTheEnvelope asserts --json goes through the
// versioned envelope and still carries no value.
func TestVaultAuditJSONCarriesTheEnvelope(t *testing.T) {
	const value = "correct-horse-battery-staple"
	deps := auditDeps(t, map[string]string{"TEAM_PASSPHRASE": value})
	stdout, _, err := runVault(t, deps, "", "audit", "--json")
	if err != nil {
		t.Fatalf("vault audit --json: %v", err)
	}
	var envelope map[string]interface{}
	if jerr := json.Unmarshal([]byte(stdout), &envelope); jerr != nil {
		t.Fatalf("the --json output is not JSON: %v\n%s", jerr, stdout)
	}
	// CONTRADICTION, recorded in this ticket's journal: the contract's own
	// check greps for a "schema" key. internal/output's versioned envelope
	// (D/S-06.T5) has no such key - it is {version, ok, data}. The tree
	// wins, so this asserts the envelope the repo actually emits.
	if _, ok := envelope["version"]; !ok {
		t.Fatalf("the --json output carries no envelope version field:\n%s", stdout)
	}
	if _, ok := envelope["data"]; !ok {
		t.Fatalf("the --json output carries no envelope data field:\n%s", stdout)
	}
	if strings.Contains(stdout, value) {
		t.Fatalf("the --json surface printed a stored value:\n%s", stdout)
	}
}

// TestVaultAuditHasNoRevealFlag pins that value access stays on the
// elevated verb: nothing on this surface offers to print one.
func TestVaultAuditHasNoRevealFlag(t *testing.T) {
	stdout, _, err := runVault(t, auditDeps(t, nil), "", "audit", "--help")
	if err != nil {
		t.Fatalf("vault audit --help: %v", err)
	}
	for _, forbidden := range []string{"reveal", "--show-values", "--values"} {
		if strings.Contains(strings.ToLower(stdout), forbidden) {
			t.Fatalf("`vault audit --help` offers %q:\n%s", forbidden, stdout)
		}
	}
}

func TestVaultAuditRejectsArguments(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "", "audit", "extra"); err == nil {
		t.Fatal("vault audit accepted a positional argument")
	}
}
