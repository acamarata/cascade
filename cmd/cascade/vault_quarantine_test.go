package main

// Purpose: the CLI half of the quarantine surface - listing what the
//
//	detector flagged, promoting an entry into the vault, and releasing
//	one the detector got wrong.
//
// Constraints: every assertion here is about metadata and about the two
//
//	exits from quarantine. The promotion test also acts as a canary: the
//	rendered output of a promote must not contain the value that was just
//	stored.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
)

// seedQuarantine records one detection through the same store the CLI
// will open, and returns its id.
func seedQuarantine(t *testing.T, deps vaultDeps, name string) string {
	t.Helper()
	store, err := deps.Quarantine()
	if err != nil {
		t.Fatalf("opening the quarantine store: %v", err)
	}
	entry, err := store.Put(secrets.DetectionHit{
		Class: secrets.ClassAPIKey, Pattern: "vendor-api-key-prefix",
		Offset: 12, Len: 40, Confidence: secrets.ConfidenceCertain, SuggestedName: name,
	}, "memory:note:42", []byte("value-bytes"))
	if err != nil {
		t.Fatalf("seeding the quarantine entry: %v", err)
	}
	return entry.ID
}

// TestVaultPromoteFromQuarantine is the contract's named check: an entry
// is promoted into the vault under its suggested name, the value comes
// from stdin, and the entry is gone from quarantine afterwards.
func TestVaultPromoteFromQuarantine(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	id := seedQuarantine(t, deps, "PROMOTED_TOKEN")

	stdout, _, err := runVault(t, deps, "s3cr3t-value\n", "set", "--from-quarantine", id, "--json")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if strings.Contains(stdout, "s3cr3t-value") {
		t.Fatalf("the promote output carried the value: %s", stdout)
	}
	if !strings.Contains(stdout, "PROMOTED_TOKEN") {
		t.Fatalf("the promote output does not name the secret: %s", stdout)
	}

	listed, _, err := runVault(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listed, "PROMOTED_TOKEN") {
		t.Fatalf("the promoted name is not in the vault: %s", listed)
	}

	remaining, _, err := runVault(t, deps, "", "quarantine", "list", "--json")
	if err != nil {
		t.Fatalf("quarantine list: %v", err)
	}
	if strings.Contains(remaining, id) {
		t.Fatalf("the promoted entry is still quarantined: %s", remaining)
	}
}

// TestVaultPromoteUnknownID: a typo must be a refusal, never a silent
// creation of an empty secret.
func TestVaultPromoteUnknownID(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "v\n", "set", "--from-quarantine", "no-such-id"); err == nil {
		t.Fatal("an unknown quarantine id was accepted")
	}
}

// TestVaultPromoteRefusesUnpipedNoInput covers the CASCADE_NO_INPUT=1
// rule: with nothing piped in there is no value to store, and prompting
// is forbidden, so the command must fail rather than store an empty one.
func TestVaultPromoteRefusesUnpipedNoInput(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	deps.StdinIsPiped = func() bool { return false }
	id := seedQuarantine(t, deps, "NO_INPUT_TOKEN")
	_, _, err := runVault(t, deps, "", "set", "--from-quarantine", id)
	if err == nil {
		t.Fatal("promotion succeeded with no piped stdin under CASCADE_NO_INPUT=1")
	}
	if !strings.Contains(err.Error(), "CASCADE_NO_INPUT") {
		t.Fatalf("the refusal does not explain itself: %v", err)
	}
}

// TestVaultPromoteRefusesAName: the name comes from the entry, so passing
// one too is ambiguous and is refused rather than silently resolved.
func TestVaultPromoteRefusesAName(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	id := seedQuarantine(t, deps, "AMBIGUOUS_TOKEN")
	if _, _, err := runVault(t, deps, "v\n", "set", "--from-quarantine", id, "OTHER_NAME"); err == nil {
		t.Fatal("a NAME alongside --from-quarantine was accepted")
	}
}

// TestVaultSetStillNeedsAName: relaxing the argument count for
// --from-quarantine must not make a bare `vault set` legal.
func TestVaultSetStillNeedsAName(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "v\n", "set"); err == nil {
		t.Fatal("`vault set` with no NAME was accepted")
	}
}

// TestVaultQuarantineListShowsMetadataOnly: the listing must identify a
// finding by location and shape, and must carry no value field at all.
func TestVaultQuarantineListShowsMetadataOnly(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	id := seedQuarantine(t, deps, "LISTED_TOKEN")
	stdout, _, err := runVault(t, deps, "", "quarantine", "list", "--json")
	if err != nil {
		t.Fatalf("quarantine list: %v", err)
	}
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "api-key") {
		t.Fatalf("the listing does not identify the finding: %s", stdout)
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("the listing is not valid JSON: %v", err)
	}
	for _, forbidden := range []string{"\"value\"", "value-bytes"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("the listing carries %q: %s", forbidden, stdout)
		}
	}
}

// TestVaultQuarantineRelease is the false-positive recovery path: the
// entry goes away, and releasing an unknown id is an error.
func TestVaultQuarantineRelease(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	id := seedQuarantine(t, deps, "WRONGLY_FLAGGED")
	if _, _, err := runVault(t, deps, "", "quarantine", "release", id); err != nil {
		t.Fatalf("release: %v", err)
	}
	stdout, _, err := runVault(t, deps, "", "quarantine", "list", "--json")
	if err != nil {
		t.Fatalf("quarantine list: %v", err)
	}
	if strings.Contains(stdout, id) {
		t.Fatalf("the released entry is still listed: %s", stdout)
	}
	if _, _, err := runVault(t, deps, "", "quarantine", "release", id); err == nil {
		t.Fatal("releasing an already-released entry succeeded")
	}
}

// TestVaultQuarantineWithoutAStore: an unconfigured provider is an
// internal error, not a pretend-empty listing.
func TestVaultQuarantineWithoutAStore(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	deps.Quarantine = nil
	if _, _, err := runVault(t, deps, "", "quarantine", "list"); err == nil {
		t.Fatal("an unconfigured quarantine store listed successfully")
	}
	if _, _, err := runVault(t, deps, "v\n", "set", "--from-quarantine", "id"); err == nil {
		t.Fatal("an unconfigured quarantine store promoted successfully")
	}
}
