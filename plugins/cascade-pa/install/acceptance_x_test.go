// Package install_test holds the Epic X acceptance story
// (P1-E24-W5-S50-T7) as an EXTERNAL test package, not "package install"
// like this directory's other _test.go files: acceptance_x_installer_test.go
// and acceptance_x_elevation_test.go import internal/plugins and
// internal/elevation/internal/rpc respectively, and internal/plugins
// itself imports plugins/cascade-pa/install (its composition-root files,
// e.g. cascadepa_install_confirm.go) -- an internal "package install"
// test file importing internal/plugins would close that into an import
// cycle ("import cycle not allowed in test", verified empirically). An
// external "package install_test" file has no such problem: nothing
// imports install_test, so install_test -> internal/plugins -> install
// is a DAG, not a cycle. Every install-package symbol below is therefore
// qualified with "install." -- all of them are exported (Deps, NewFlow,
// the Event vocabulary, the Installer/Elevator/ConfirmGate seams), so
// nothing here reaches into the production package's unexported state.
package install_test

// Purpose (this file): the acceptance story itself: "link my GitHub"
//   resolves against a real, signed registry fixture (S-50.T1), proposes
//   cascade-github (Y/S-51.T1's real manifest), confirms, satisfies
//   elevation through the real D/S-07.T6 local elevation broker
//   (acceptance_x_elevation_test.go), and drives the real plugin-add
//   lifecycle (acceptance_x_installer_test.go) -- every collaborator is
//   real production code, composed through install.Deps's own seams
//   exactly as a caller outside cascade-pa would (Art.10.2: cascade-pa
//   never imports internal/, so production flow.go never sees any of
//   these test-local wrappers).
//
// HONEST GAP (read before trusting a green run to mean "tools are live"):
//   cascade-github is runtime="process" (plugins/github/manifest.toml).
//   internal/plugins/dispatch.go's ProvisionElevated ALWAYS refuses a
//   RuntimeProcess candidate: it calls the real
//   process.NewProcessRuntime().Launch(ctx, process.Manifest{TrustTier:
//   process.TrustTierUntrusted}) and, by that file's own documented
//   finding, "no code path in this tree ever sets a manifest's trust tier
//   above the default" -- so Launch always refuses, and the elevated
//   install never actually completes for ANY process-tier plugin today.
//   Separately, internal/mcp/registry.go's ToolRegistry sources tools
//   from exactly one ManifestSource in production: pkg/plugin.Builtins,
//   the compile-time builtin registry (registry.go:49-55,144-149) --
//   cascade-github is never compiled in (internal/plugins/
//   builtin_tier_only_test.go asserts exactly that), so no seam in this
//   tree folds an installed process-tier plugin's manifest into the MCP
//   tool surface at all, mounted or not. cmd/cascade/plugin_process_mount.go
//   (P1-E25-W5-S51-T3) closed the analogous gap for CLI verbs (cascade
//   github ...) but that is a compiled-in cobra mount, not a live-list or
//   MCP-surface seam. TestAcceptance_X_LinkGitHub therefore proves the
//   flow through the real elevation broker and the real, CURRENT refusal
//   at the process-tier trust gate: the quality constitution (Art.1, Art.2
//   §1) forbids a stand-in host or a fabricated success, so the suite
//   asserts exactly what the real tree does and records the shortfall.
//   TestAcceptance_X_AlreadyInstalled below reaches a genuine successful
//   resume (its candidate is pre-recorded as already-installed, the one
//   path plugins.AddPlugin's own idempotency short-circuit satisfies
//   before elevation is ever touched) so the suite still proves a real,
//   passing end-to-end resume for the case the real tree can complete.
//
// SPORT: plugins/cascade-pa/install:acceptance (ADD) -- P1-E24-W5-S50-T7.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// fixedAcceptTestTime is the frozen instant every clock in this suite
// uses (Art.7.3: no sleeps, no bare time.Now).
var fixedAcceptTestTime = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

// acceptancePublicKeyB64 is the fixture signing key's public half; see
// testdata/acceptance/README.md for full generation provenance.
const acceptancePublicKeyB64 = "14rm+t9vx2h0KKOhATbZmLDlzwLAhDBKiz+rKKUoJwM="

// acceptIntent is the intent string driving this whole suite -- an exact
// match against the fixture's "link my github" tag (registryTier's
// rankExactTag tier).
const acceptIntent = "link my GitHub"

func acceptVerifier() plugin.Ed25519Verifier {
	pub, err := base64.StdEncoding.DecodeString(acceptancePublicKeyB64)
	if err != nil {
		panic("acceptVerifier: bad fixture public key: " + err.Error())
	}
	return plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(pub)}
}

// acceptVerifiedIndex loads and verifies the real, signed acceptance
// fixture -- the SAME plugin.NewVerifiedIndex a production caller uses,
// over bytes read from disk (never fetched live during CI, Art.7 §2).
func acceptVerifiedIndex(t *testing.T) *plugin.VerifiedIndex {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "acceptance", "index.json"))
	if err != nil {
		t.Fatalf("acceptVerifiedIndex: read fixture: %v", err)
	}
	idx, err := plugin.NewVerifiedIndex(context.Background(), acceptVerifier(), data)
	if err != nil {
		t.Fatalf("acceptVerifiedIndex: verify fixture: %v", err)
	}
	return idx
}

// acceptRealArtifact reads the REAL, shipped Y/S-51.T1 cascade-github
// manifest straight from the repo tree -- never a private copy under
// testdata/, so it can never silently drift from what actually ships
// (see testdata/acceptance/README.md's drift note).
func acceptRealArtifact(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "github", "manifest.toml"))
	if err != nil {
		t.Fatalf("acceptRealArtifact: read plugins/github/manifest.toml: %v", err)
	}
	return data
}

// TestAcceptance_X_LinkGitHub drives the real end-to-end flow for a
// fresh cascade-github install: resolve -> propose -> confirm -> elevate
// -> the real, current process-tier refusal (see this file's HONEST GAP
// header). Two subtests separate "no broker at all" (R-14.72's literal
// requirement) from "a genuine broker satisfied the elevation step" --
// each factored into its own top-level helper (funlen: 50-line cap).
func TestAcceptance_X_LinkGitHub(t *testing.T) {
	artifact := acceptRealArtifact(t)
	t.Run("BrokerWithheld_InstallRefused", func(t *testing.T) {
		acceptLinkGitHubBrokerWithheld(t, artifact)
	})
	t.Run("BrokerSatisfied_RealProcessTierGateRefuses", func(t *testing.T) {
		acceptLinkGitHubBrokerSatisfied(t, artifact)
	})
}

// acceptLinkGitHubBrokerWithheld is R-14.72's literal case: no local
// elevation broker configured at all -- the elevation-required probe
// fires once and the run refuses, never retrying.
func acceptLinkGitHubBrokerWithheld(t *testing.T, artifact []byte) {
	ctx := context.Background()
	idx := acceptVerifiedIndex(t)
	installer := newAcceptRegistryInstaller(t, acceptVerifier(), artifact)
	confirm := &acceptConfirm{outcome: install.ConfirmYes}
	bus := &acceptBus{}
	f := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: acceptWithheldElevator{}, Events: bus})

	result, err := f.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: idx})
	if err == nil {
		t.Fatal("Run() = nil error with no elevation broker configured, want R-14.72's refusal")
	}
	if result.Resumed {
		t.Fatal("Resumed = true with the elevation broker withheld, want it refused")
	}
	if len(installer.calls) != 1 {
		t.Fatalf("Installer.Add called %d times, want exactly 1 (the elevation-required probe, no retry without a broker)", len(installer.calls))
	}
	if bus.has(install.EventConversationResume) {
		t.Fatal("EventConversationResume published with the broker withheld")
	}
}

// acceptLinkGitHubBrokerSatisfied drives the real D/S-07.T6 elevation
// broker to a genuine, verified approval, then proves the real
// process-tier trust gate (dispatch.go's ProvisionElevated) is what
// refuses the retried install -- see this file's HONEST GAP header.
func acceptLinkGitHubBrokerSatisfied(t *testing.T, artifact []byte) {
	ctx := context.Background()
	idx := acceptVerifiedIndex(t)
	installer := newAcceptRegistryInstaller(t, acceptVerifier(), artifact)
	confirm := &acceptConfirm{outcome: install.ConfirmYes}
	bus := &acceptBus{}
	elevator := newAcceptElevator(t)
	f := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: elevator, Events: bus})

	result, err := f.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: idx, ThreadID: "acceptance-x-thread"})
	if err == nil {
		t.Fatal("Run() = nil error, want the real process-tier trust-gate refusal (dispatch.go's documented gap)")
	}
	if result.Resumed {
		t.Fatal("Resumed = true, want no resume after the real refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Fatalf("error kind = %v (ok=%v), want KindPolicyDenied (ProvisionElevated's untrusted-tier refusal)", kind, ok)
	}
	if bus.events[0].Kind != install.EventInstallProposal {
		t.Fatalf("first event = %v, want EventInstallProposal before any install side effect (CLIENT-LOCAL ECHO)", bus.events[0])
	}
	if confirm.calls != 1 {
		t.Fatalf("Confirm.Confirm called %d times, want exactly 1", confirm.calls)
	}
	if !bus.has(install.EventElevationRequired) {
		t.Fatal("events missing EventElevationRequired -- cascade-github is process-tier and must require elevation")
	}
	acceptAssertWitnessedRetry(t, installer, bus)
}

// acceptAssertWitnessedRetry is R-14.72's proof: the elevation step must
// occur BEFORE the process-tier install proceeds. Exactly two Add calls,
// the first carrying no witness (the probe that discovered
// AddOutcomeElevationRequired) and the second carrying a witness
// VerifyElevationWitness minted only after the real attestation verified
// -- a caller that merely asserts "elevated" can never produce
// Witness.Valid() == true (elevation.go's own contract).
func acceptAssertWitnessedRetry(t *testing.T, installer *acceptRegistryInstaller, bus *acceptBus) {
	if len(installer.calls) != 2 {
		t.Fatalf("Installer.Add called %d times, want exactly 2 (probe, then the witnessed retry)", len(installer.calls))
	}
	if installer.calls[0].Witness.Valid() {
		t.Fatal("the FIRST Add call already carried a valid witness -- elevation must not precede the probe that discovers it is required")
	}
	if !installer.calls[1].Witness.Valid() {
		t.Fatal("the SECOND Add call carries no valid witness -- the real elevation broker's approval never reached the retried install")
	}
	last := bus.events[len(bus.events)-1]
	if last.Kind != install.EventInstallFailed || last.Failed == nil || last.Failed.Reason != cascade.KindPolicyDenied {
		t.Fatalf("last event = %v, want EventInstallFailed{Reason: KindPolicyDenied}", last)
	}
	if _, ok, err := plugins.LoadMetadata(context.Background(), installer.store, "cascade-github"); err != nil || ok {
		t.Fatalf("LoadMetadata(cascade-github) = (ok=%v, err=%v), want ok=false -- the process-tier refusal must leave zero installed-metadata record (no fabricated 'live' plugin)", ok, err)
	}
}
