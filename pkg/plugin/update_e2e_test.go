// Purpose: TestPluginUpdateGrantDiffE2e, split from update_test.go purely
//
//	to stay under Art.10.3's 300-line cap (registry_search.go/
//	registry_client.go's own established split precedent) — every shared
//	fixture/fetcher/helper it uses (grantDiffFixtureBytes, grantDiffClient,
//	updateFakeFetcher, grantDiffCandidateManifestTOML, assertStagingEmpty)
//	is declared in update_test.go, same package. Each acceptance point is
//	its own <=50-line helper (Art.10.3's function cap) called from the
//	top-level test via t.Run.
//
// SCOPE DEVIATION FROM THE TICKET'S OWN files_scope (recorded per
// LANE-RULES §1, precedent pkg/plugin/registry_tamper_test.go's own
// CONTRACT-VS-TREE note): files_scope names one test file,
// pkg/plugin/registry/update_test.go (aliased to pkg/plugin/update_test.go
// by the ticket's own execution_guidance). This second file exists only
// to keep update_test.go under the 300-line cap while still landing every
// named check (TestCheckUpdate, TestPluginUpdateGrantDiffE2e) — the same
// reason registry_client_test.go/registry_tamper_test.go/
// registry_example_test.go/registry_fuzz_test.go are four separate files
// for what the tickets that authored them each called "the" test file.
//
// SPORT: pkg/plugin registry-client-update tests (ADD) — P1-E24-W5-S50-T8.
package plugin_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// grantDiffE2EFixture builds the shared client and the map of DownloadURL
// to real candidate manifest bytes every subtest below reuses.
func grantDiffE2EFixture(t *testing.T) (*plugin.RegistryClient, map[string][]byte) {
	t.Helper()
	artifacts := map[string][]byte{
		"https://registry.example.com/grant-diff-demo/1.1.0.plugin": []byte(grantDiffCandidateManifestTOML),
	}
	client := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true, artifacts: artifacts})
	return client, artifacts
}

// TestPluginUpdateGrantDiffE2e exercises the full registry+verify+grant-
// diff decision chain over the real provenance-stamped fixture, asserting
// the ticket's five acceptance points. The CLI flags this maps to
// (--yes/--accept-grant/CASCADE_NO_INPUT) and the actual store commit are
// cmd/cascade/plugin_update_test.go's TestPluginUpdateCommand — this
// function proves the pkg/plugin-level decisions those flags drive.
func TestPluginUpdateGrantDiffE2e(t *testing.T) {
	client, _ := grantDiffE2EFixture(t)

	// (5) already-at-latest idempotency, checked first so it cannot be
	// contaminated by state the rest of this test creates.
	t.Run("already_at_latest_idempotent", func(t *testing.T) {
		entry, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.1.0")
		if err != nil || entry != nil {
			t.Fatalf("CheckUpdate(installed=latest) = (%+v, %v), want (nil, nil) — no update available", entry, err)
		}
	})

	entry, artifact := grantDiffE2EFetchCandidate(t, client)
	added, removed := grantDiffE2EComputeDiff(t, artifact) // (1) grant diff is presented.

	t.Run("yes_without_accept_grant_fails", func(t *testing.T) { // (2)
		grantDiffE2EAssertRejectedWithoutAccept(t, added)
	})
	t.Run("yes_with_accept_grant_succeeds", func(t *testing.T) { // (3)
		if err := plugin.ConfirmGrantAcceptance(added, []string{"network.egress"}); err != nil {
			t.Fatalf("ConfirmGrantAcceptance(added, [network.egress]) = %v, want nil", err)
		}
	})
	t.Run("checksum_mismatch_cleans_staging", func(t *testing.T) { // (4)
		grantDiffE2EAssertChecksumMismatchRollsBack(t, *entry)
	})
	if len(removed) != 0 {
		t.Fatalf("GrantDiff removed = %v, want none", removed)
	}
}

// grantDiffE2EFetchCandidate runs CheckUpdate(1.0.0) then
// StageAndVerifyArtifact, asserting the staging file is always cleaned up
// and the candidate manifest parses.
func grantDiffE2EFetchCandidate(t *testing.T, client *plugin.RegistryClient) (*plugin.RegistryVersionEntry, plugin.Manifest) {
	t.Helper()
	entry, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.0.0")
	if err != nil {
		t.Fatalf("CheckUpdate(1.0.0): %v", err)
	}
	if entry == nil {
		t.Fatal("CheckUpdate(1.0.0) = nil, want the 1.1.0 candidate entry")
	}
	stagingDir := t.TempDir()
	data, err := client.StageAndVerifyArtifact(context.Background(), *entry, stagingDir)
	if err != nil {
		t.Fatalf("StageAndVerifyArtifact(candidate): %v", err)
	}
	assertStagingEmpty(t, stagingDir)
	m, err := plugin.ParseManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ParseManifest(verified candidate artifact): %v", err)
	}
	return entry, m
}

// grantDiffE2EComputeDiff asserts the exact single-capability diff this
// fixture is provenance-stamped to produce.
func grantDiffE2EComputeDiff(t *testing.T, m plugin.Manifest) (added, removed []string) {
	t.Helper()
	added, removed = plugin.GrantDiff([]string{"storage.local"}, m.Requires)
	if len(added) != 1 || added[0] != "network.egress" {
		t.Fatalf("GrantDiff added = %v, want [network.egress]", added)
	}
	return added, removed
}

// grantDiffE2EAssertRejectedWithoutAccept is acceptance point (2): --yes
// without --accept-grant fails on grant expansion.
func grantDiffE2EAssertRejectedWithoutAccept(t *testing.T, added []string) {
	t.Helper()
	err := plugin.ConfirmGrantAcceptance(added, nil)
	if err == nil {
		t.Fatal("ConfirmGrantAcceptance(added, nil) = nil, want a non-nil error (grant expansion never auto-accepted)")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ConfirmGrantAcceptance err = %v, want KindInvalidInput", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("network.egress")) {
		t.Fatalf("ConfirmGrantAcceptance err = %v, want it to name the missing grant", err)
	}
}

// grantDiffE2EAssertChecksumMismatchRollsBack is acceptance point (4):
// tampered artifact bytes fail verification and the staging file is
// cleaned up regardless.
func grantDiffE2EAssertChecksumMismatchRollsBack(t *testing.T, entry plugin.RegistryVersionEntry) {
	t.Helper()
	tamperedArtifacts := map[string][]byte{
		"https://registry.example.com/grant-diff-demo/1.1.0.plugin": []byte("these are not the real candidate bytes"),
	}
	tamperedClient := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true, artifacts: tamperedArtifacts})
	dir := t.TempDir()
	_, err := tamperedClient.StageAndVerifyArtifact(context.Background(), entry, dir)
	if err == nil {
		t.Fatal("StageAndVerifyArtifact over tampered bytes = nil error, want ErrChecksumMismatch")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("StageAndVerifyArtifact err = %v, want KindIntegrity", err)
	}
	assertStagingEmpty(t, dir)
}
