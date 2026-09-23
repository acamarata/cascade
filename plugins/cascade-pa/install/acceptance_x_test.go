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
//   MCP-surface seam. The completed-ceremony proof of that real, CURRENT
//   refusal at the process-tier trust gate (the quality constitution's
//   Art.1/Art.2 §1 forbid a stand-in host or a fabricated success) is
//   architecturally unreachable on Windows -- platformElevationRefusal
//   (internal/rpc/elevation_windows.go) preempts the attestation ceremony
//   with a nonce-less refusal before it ever completes (ci-fix12's same
//   finding for cascadepa_install_elevator.go) -- so it lives in
//   acceptance_x_posix_test.go (`!windows`); acceptance_x_windows_test.go
//   proves the Windows-side clean tier-2 refusal instead (ci-fix13).
//   TestAcceptance_X_LinkGitHub below covers only the no-broker-at-all
//   case, which needs no ceremony and runs on every platform.
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

	"github.com/acamarata/cascade/internal/plugins/resolver"
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

// TestAcceptance_X_LinkGitHub drives R-14.72's literal "no broker at all"
// case: the elevation-required probe fires once and the run refuses,
// never retrying. The "a genuine broker satisfied the elevation step"
// case needs a completed attestation ceremony that is architecturally
// unreachable on Windows (this file's HONEST GAP header), so it is a
// separate, platform-split test: acceptance_x_posix_test.go (`!windows`)
// and acceptance_x_windows_test.go (`windows`).
func TestAcceptance_X_LinkGitHub(t *testing.T) {
	artifact := acceptRealArtifact(t)
	t.Run("BrokerWithheld_InstallRefused", func(t *testing.T) {
		acceptLinkGitHubBrokerWithheld(t, artifact)
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
