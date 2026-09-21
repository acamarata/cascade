package install_test

// Purpose (this file): the Epic X acceptance error paths -- registry
//   fetch failure, checksum mismatch, and explicit user decline -- each
//   proving zero install attempts and no conversation.resume, per this
//   ticket's acceptance_criteria and Art.3's error-path requirement.
// SPORT: plugins/cascade-pa/install:acceptance (ADD) -- P1-E24-W5-S50-T7.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// acceptFailingFetcher stands in for the real HTTP transport
// (internal/plugins/registryfetch), which this untagged, no-network test
// may not import (Art.7 §2 / TestNoNetworkUnitTest forbids "net" and
// "net/http" outright). It returns the REAL T1 sentinel
// plugin.ErrRegistryHTTP unchanged, proving RegistryClient.Fetch
// propagates it verbatim rather than wrapping or losing it.
type acceptFailingFetcher struct{}

func (acceptFailingFetcher) FetchIndex(context.Context) ([]byte, error) {
	return nil, plugin.ErrRegistryHTTP
}
func (acceptFailingFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, plugin.ErrRegistryHTTP
}

// TestAcceptance_X_RegistryFetchFailure: the real T1 RegistryClient
// propagates ErrRegistryHTTP unchanged, and a caller that (correctly)
// never obtains a VerifiedIndex from a failed fetch drives the real Flow
// with a nil Index -- which fails closed via plugin.ErrUnverifiedIndex
// (06-FORGE-SPEC.md §5.20) before Confirm or Install is ever touched.
func TestAcceptance_X_RegistryFetchFailure(t *testing.T) {
	ctx := context.Background()
	client := plugin.NewRegistryClient(plugin.RegistryConfig{RegistryURL: "unused"},
		acceptFailingFetcher{}, acceptVerifier(), nil, testkit.NewFrozenClock(fixedAcceptTestTime))

	if _, err := client.Fetch(ctx); err != plugin.ErrRegistryHTTP { //nolint:errorlint // identity compare: the fetcher returns the sentinel itself, unwrapped
		t.Fatalf("RegistryClient.Fetch() error = %v, want plugin.ErrRegistryHTTP by identity", err)
	}

	installer := newAcceptRegistryInstaller(t, acceptVerifier(), acceptRealArtifact(t))
	confirm := &acceptConfirm{outcome: install.ConfirmYes}
	bus := &acceptBus{}
	f := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: acceptWithheldElevator{}, Events: bus})

	if _, err := f.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: nil}); err != plugin.ErrUnverifiedIndex { //nolint:errorlint // documented identity-compare sentinel (pkg/plugin/intents.go)
		t.Fatalf("Run() error = %v, want plugin.ErrUnverifiedIndex", err)
	}
	if confirm.calls != 0 || len(installer.calls) != 0 || len(bus.events) != 0 {
		t.Fatalf("side effects after a failed registry fetch: confirm=%d install=%d events=%v, want none",
			confirm.calls, len(installer.calls), bus.events)
	}
}

// TestAcceptance_X_ChecksumMismatch: the fixture's index (and signature)
// verify fine, but the artifact bytes handed to the installer do not
// match the entry's declared checksum -- the real
// plugin.Ed25519Verifier.VerifyArtifact (T6) refuses before
// plugins.AddPlugin is ever called, so zero bytes are written to the
// store.
func TestAcceptance_X_ChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	idx := acceptVerifiedIndex(t)
	tampered := append(append([]byte{}, acceptRealArtifact(t)...), 0x00)
	installer := newAcceptRegistryInstaller(t, acceptVerifier(), tampered)
	confirm := &acceptConfirm{outcome: install.ConfirmYes}
	bus := &acceptBus{}
	f := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: acceptWithheldElevator{}, Events: bus})

	_, err := f.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: idx})
	if err == nil {
		t.Fatal("Run() = nil error over a tampered artifact, want the checksum-mismatch refusal")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindIntegrity || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %v (kind=%v), want a KindIntegrity checksum-mismatch error (plugin.ErrChecksumMismatch)", err, kind)
	}
	if _, ok, lerr := plugins.LoadMetadata(ctx, installer.store, "cascade-github"); lerr != nil || ok {
		t.Fatalf("LoadMetadata(cascade-github) = (ok=%v, err=%v), want ok=false -- zero install attempts", ok, lerr)
	}
	if bus.has(install.EventConversationResume) {
		t.Fatal("EventConversationResume published after a checksum-mismatch refusal")
	}
}

// TestAcceptance_X_UserDecline: an explicit "no" produces zero install
// attempts and no resume.
func TestAcceptance_X_UserDecline(t *testing.T) {
	ctx := context.Background()
	idx := acceptVerifiedIndex(t)
	installer := newAcceptRegistryInstaller(t, acceptVerifier(), acceptRealArtifact(t))
	confirm := &acceptConfirm{outcome: install.ConfirmDeclined}
	bus := &acceptBus{}
	f := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: acceptWithheldElevator{}, Events: bus})

	result, err := f.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: idx})
	if err != nil {
		t.Fatalf("Run() error = %v, want a clean non-resuming return on decline", err)
	}
	if result.Resumed {
		t.Fatal("Resumed = true on an explicit decline")
	}
	if len(installer.calls) != 0 {
		t.Fatalf("Installer.Add called %d times on decline, want 0 (no side effect)", len(installer.calls))
	}
	if !bus.has(install.EventInstallDeclined) || bus.has(install.EventConversationResume) {
		t.Fatalf("events = %v, want Declined and no resume", bus.events)
	}
}
