//go:build !windows

// Purpose (this file): the one acceptance subtest that needs the full real
// elevation ceremony to actually complete (a genuine nonce-issue,
// local-auth-sign, and attestation-verify round trip all succeeding, via
// acceptance_x_elevation_test.go's newAcceptElevator), so it can reach and
// prove the real process-tier trust-gate refusal (dispatch.go's
// ProvisionElevated) beyond it. That completion is architecturally
// unreachable on Windows: platformElevationRefusal
// (internal/rpc/elevation_windows.go) preempts acceptIssueChallenge
// (acceptance_x_elevation_test.go) with a nonce-less ELEVATION_REQUIRED
// refusal before the real ceremony ever completes
// (internal/rpc/elevation_flow_windows_test.go), so this file carries the
// same `!windows` tag as internal/plugins/
// cascadepa_install_elevator_posix_test.go's analogous split (ci-fix12)
// and this file's own header mirrors that precedent exactly. Its
// Windows-side counterpart -- the same ceremony provably refusing cleanly,
// per R-14.131 -- is acceptance_x_windows_test.go. Shared fixtures
// (acceptRealArtifact, acceptVerifier, acceptVerifiedIndex,
// newAcceptRegistryInstaller, acceptConfirm, acceptBus, newAcceptElevator)
// stay untagged in their own files, since both platform variants use them.
// SPORT: plugins/cascade-pa/install:acceptance (TEST) -- P1-E24-W5-S50-T7
//
//	(CI fix ci-fix13).
package install_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// TestAcceptance_X_LinkGitHub_BrokerSatisfied drives the real D/S-07.T6
// elevation broker to a genuine, verified approval, then proves the real
// process-tier trust gate (dispatch.go's ProvisionElevated) is what
// refuses the retried install -- see acceptance_x_test.go's HONEST GAP
// header.
func TestAcceptance_X_LinkGitHub_BrokerSatisfied(t *testing.T) {
	artifact := acceptRealArtifact(t)
	t.Run("BrokerSatisfied_RealProcessTierGateRefuses", func(t *testing.T) {
		acceptLinkGitHubBrokerSatisfied(t, artifact)
	})
}

// acceptLinkGitHubBrokerSatisfied is the subtest body (funlen: 50-line
// cap keeps it a separate top-level helper).
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
