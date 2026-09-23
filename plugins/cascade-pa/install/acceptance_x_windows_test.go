//go:build windows

// Purpose (this file): proves, on the Windows lane itself (R-14.131: a
// platform-specific behavior needs a test that RUNS on that platform, not
// merely builds), that the Epic X acceptance flow's real elevation
// ceremony (acceptance_x_elevation_test.go's newAcceptElevator, the SAME
// broker acceptance_x_posix_test.go's completed-ceremony proof uses)
// refuses cleanly with elevation.ErrWindowsTier2 when
// platformElevationRefusal (internal/rpc/elevation_windows.go) preempts
// it. Before the fix (acceptIssueChallenge,
// acceptance_x_elevation_test.go), this exact case mislabeled the
// platform's by-design nonce-less refusal
// (internal/rpc/elevation_flow_windows_test.go) as
// `cascade.KindIntegrity "acceptance elevator: elevation challenge has no
// nonce"` -- the CI failure this file's fix and the
// acceptance_x_posix_test.go build-tag split together resolve (ci-fix13).
// Mirrors internal/plugins/cascadepa_install_elevator_windows_test.go and
// cmd/cascade/backup_windows_tier2_test.go exactly (P1-E19-W4-S42-T3,
// ci-fix12).
// SPORT: plugins/cascade-pa/install:acceptance (TEST) -- P1-E24-W5-S50-T7
//
//	(CI fix ci-fix13).
package install_test

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// TestAcceptance_X_LinkGitHub_BrokerSatisfiedWindowsTier2Refuses drives
// the same real broker acceptance_x_posix_test.go's
// TestAcceptance_X_LinkGitHub_BrokerSatisfied does, but on Windows the
// ceremony itself never completes: the initial elevation-required probe
// fires, then Elevate refuses by name (ErrWindowsTier2) before any
// witnessed retry is ever attempted.
func TestAcceptance_X_LinkGitHub_BrokerSatisfiedWindowsTier2Refuses(t *testing.T) {
	ctx := context.Background()
	artifact := acceptRealArtifact(t)
	idx := acceptVerifiedIndex(t)
	installer := newAcceptRegistryInstaller(t, acceptVerifier(), artifact)
	confirm := &acceptConfirm{outcome: install.ConfirmYes}
	bus := &acceptBus{}
	elevator := newAcceptElevator(t) // enrolled, real keystore -- the wire-level probe is what refuses on Windows, not Tier()
	flow := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: elevator, Events: bus})

	result, err := flow.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: idx, ThreadID: "acceptance-x-windows-thread"})
	acceptAssertWindowsTier2Refusal(t, ctx, err, result, installer, bus)
}

// acceptAssertWindowsTier2Refusal is this test's assertion body (funlen:
// 50-line cap keeps it a separate top-level helper, matching this file's
// POSIX-side sibling acceptAssertWitnessedRetry).
func acceptAssertWindowsTier2Refusal(t *testing.T, ctx context.Context, err error, result install.RunResult,
	installer *acceptRegistryInstaller, bus *acceptBus) {
	t.Helper()
	if err == nil {
		t.Fatal("Run() = nil error on Windows, want the tier-2 refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("error kind = %v (ok=%v), want KindUnsupported (elevation.ErrWindowsTier2)", kind, ok)
	}
	// this repo's cascade sentinels compare Kind only (errors.Is on two
	// cascade.New errors of the same Kind is not identity), so also
	// assert the message never regresses to the pre-fix mislabel.
	if strings.Contains(err.Error(), "no nonce") {
		t.Fatalf("Run() on Windows leaked the nonce-less-challenge integrity message instead of the tier-2 refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "tier-2") {
		t.Fatalf("Run() on Windows: err = %v, want elevation.ErrWindowsTier2's tier-2 message", err)
	}
	if result.Resumed {
		t.Fatal("Resumed = true on Windows, want no resume after the tier-2 refusal")
	}
	if bus.events[0].Kind != install.EventInstallProposal {
		t.Fatalf("first event = %v, want EventInstallProposal before any install side effect (CLIENT-LOCAL ECHO)", bus.events[0])
	}
	if len(installer.calls) != 1 {
		t.Fatalf("Installer.Add called %d times, want exactly 1 (the elevation-required probe; the tier-2 refusal precedes any witnessed retry)", len(installer.calls))
	}
	if _, ok, lerr := plugins.LoadMetadata(ctx, installer.store, "cascade-github"); lerr != nil || ok {
		t.Fatalf("LoadMetadata(cascade-github) = (ok=%v, err=%v), want ok=false -- zero install attempts completed", ok, lerr)
	}
	last := bus.events[len(bus.events)-1]
	if last.Kind != install.EventInstallFailed || last.Failed == nil || last.Failed.Reason != cascade.KindUnsupported {
		t.Fatalf("last event = %v, want EventInstallFailed{Reason: KindUnsupported}", last)
	}
}
