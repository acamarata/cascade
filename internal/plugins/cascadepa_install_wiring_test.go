package plugins

// Purpose (this file): the composition root's own tests -- type assertions
//   proving buildInstallDeps binds every seam to its REAL concrete type
//   (never install's own unconfigured/discarding defaults), plus two
//   end-to-end runs (resolved intent -> proposal event on a real bus ->
//   confirm -> real installer call on a TempDir plugin store -> resume;
//   and an unapproved-elevation-refused run) driving the SAME real
//   Resolver/Installer/EventBus/Elevator this file's production
//   constructor binds. Both still swap ConfirmGate for fakeYesConfirmGate
//   (a deliberate double) -- the REAL approvalConfirmGate's own contract
//   (enqueue -> decide -> confirm) is proven end to end separately, in
//   cascadepa_install_confirm_test.go, including one full Flow run through
//   the genuine gate.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

func TestBuildInstallDeps_BindsRealConcreteTypes(t *testing.T) {
	dir := t.TempDir()
	deps := buildInstallDeps(func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil },
		testkit.NewFrozenClock(fixedInstallTestTime))

	if _, ok := deps.Install.(*installerAdapter); !ok {
		t.Errorf("Deps.Install = %T, want *installerAdapter", deps.Install)
	}
	if _, ok := deps.Elevate.(*installElevator); !ok {
		t.Errorf("Deps.Elevate = %T, want *installElevator", deps.Elevate)
	}
	if _, ok := deps.Confirm.(*approvalConfirmGate); !ok {
		t.Errorf("Deps.Confirm = %T, want *approvalConfirmGate", deps.Confirm)
	}
	multi, ok := deps.Events.(*multiEventPublisher)
	if !ok {
		t.Fatalf("Deps.Events = %T, want *multiEventPublisher", deps.Events)
	}
	if _, ok := any(multi.durable).(*busEventPublisher); !ok {
		t.Errorf("multiEventPublisher.durable = %T, want *busEventPublisher", multi.durable)
	}
	if multi.echo == nil {
		t.Error("multiEventPublisher.echo is nil, want a real *chatEchoPublisher")
	}
	wantResolver := reflect.TypeOf(resolver.NewIntentResolver())
	if gotResolver := reflect.TypeOf(deps.Resolver); gotResolver != wantResolver {
		t.Errorf("Deps.Resolver type = %v, want %v (resolver.NewIntentResolver's own concrete type)", gotResolver, wantResolver)
	}
	if deps.Getenv == nil {
		t.Error("Deps.Getenv is nil")
	}
	if deps.Snapshot == nil {
		t.Error("Deps.Snapshot is nil")
	}
}

func TestBuiltinSnapshot_ReturnsRealBuiltinsWithNilIndex(t *testing.T) {
	set, idx, err := builtinSnapshot(context.Background())
	if err != nil {
		t.Fatalf("builtinSnapshot: %v", err)
	}
	if idx != nil {
		t.Error("VerifiedIndex is non-nil -- this file's own header documents it as a deliberate, disclosed no-op")
	}
	wantLen := len(plugin.Builtins())
	if len(set) != wantLen {
		t.Errorf("len(set) = %d, want %d (plugin.Builtins() itself)", len(set), wantLen)
	}
	for _, ip := range set {
		if !ip.Enabled {
			t.Errorf("InstalledPlugin %q has Enabled=false -- every compiled-in builtin is enabled by construction", ip.Manifest.ID)
		}
	}
}

// --- end-to-end (install/elevate/events plumbing; ConfirmGate double) ---

// fakeYesConfirmGate always approves -- a deliberate double standing in
// for the ask-gate seam, so these two tests isolate the install/elevate/
// events wiring from the real ApprovalQueue's own timing. The real gate's
// contract is proven separately (cascadepa_install_confirm_test.go).
type fakeYesConfirmGate struct{ calls int }

func (f *fakeYesConfirmGate) Confirm(context.Context, install.Proposal) (install.ConfirmOutcome, error) {
	f.calls++
	return install.ConfirmYes, nil
}

// signedTestIndex builds a real signed registry index document over
// entries with a freshly generated Ed25519 key, through the real
// production verifier.
func signedTestIndex(t *testing.T, entries []plugin.RegistryIndexEntry) *plugin.VerifiedIndex {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	payload, err := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
	}{SchemaVersion: plugin.SchemaVersionCurrent, Entries: raw})
	if err != nil {
		t.Fatalf("marshal signed payload: %v", err)
	}
	doc, err := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
		Signature     string          `json:"signature"`
	}{SchemaVersion: plugin.SchemaVersionCurrent, Entries: raw,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	if err != nil {
		t.Fatalf("marshal index document: %v", err)
	}
	idx, err := plugin.NewVerifiedIndex(context.Background(), plugin.Ed25519Verifier{PublicKey: pub}, doc)
	if err != nil {
		t.Fatalf("verify signed-in-test index: %v", err)
	}
	return idx
}

const endToEndIntent = "install-test-intent"

func endToEndIndex(t *testing.T) *plugin.VerifiedIndex {
	return signedTestIndex(t, []plugin.RegistryIndexEntry{{
		ID: "no-requires-plugin", Name: "No Requires Plugin", Tags: []string{endToEndIntent},
		LatestVersion: "1.0.0", Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0"}},
	}})
}

func TestEndToEnd_ResolveProposeConfirmInstallResume(t *testing.T) {
	dir := t.TempDir()
	resolvePaths := func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil }
	clock := testkit.NewFrozenClock(fixedInstallTestTime)

	rec := &recordingEventPublisher{}
	eventBus := newBusEventPublisher(rec)

	raw, checksum, signature, pub := signedFixture(t, noRequiresManifest)
	hostStoreFixture(t, dir)
	installer := newInstallerAdapter(resolvePaths, clock, newSharedCascadeStore())
	t.Cleanup(func() { _ = installer.Close() })
	installer.fetcher = func(string) plugin.RegistryFetcher { return &fakeRegistryFetcher{artifact: raw} }
	installer.registryURL = func() (string, error) { return "https://registry.example/", nil }
	installer.registryPubKey = func() (ed25519.PublicKey, error) { return pub, nil }

	confirm := &fakeYesConfirmGate{}
	elevator := newInstallElevator(resolvePaths, clock, func(string) string { return "" })

	f := install.NewFlow(install.Deps{
		Resolver: resolver.NewIntentResolver(),
		Confirm:  confirm,
		Install:  installer,
		Elevate:  elevator,
		Events:   eventBus,
		Getenv:   func(string) string { return "" },
	})

	res, err := f.Run(context.Background(), install.RunRequest{
		Intent: endToEndIntent,
		Index: signedTestIndex(t, []plugin.RegistryIndexEntry{{
			ID: "no-requires-plugin", Name: "No Requires Plugin", Tags: []string{endToEndIntent},
			LatestVersion: "1.0.0",
			Versions:      []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}},
		}}),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Resumed {
		t.Fatal("Resumed = false, want a completed install to resume the original intent")
	}
	if confirm.calls != 1 {
		t.Fatalf("Confirm called %d times, want exactly 1", confirm.calls)
	}
	if rec.calls == 0 {
		t.Fatal("no event was published on the real bus -- durable record never happened")
	}
	if rec.namespace != installEventNamespace {
		t.Errorf("last published namespace = %q, want %q", rec.namespace, installEventNamespace)
	}
}

func TestEndToEnd_UnapprovedElevationRefused(t *testing.T) {
	dir := t.TempDir()
	resolvePaths := func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil }
	clock := testkit.NewFrozenClock(fixedInstallTestTime)

	rec := &recordingEventPublisher{}
	eventBus := newBusEventPublisher(rec)

	raw, checksum, signature, pub := signedFixture(t, grantExpandingManifest)
	hostStoreFixture(t, dir)
	installer := newInstallerAdapter(resolvePaths, clock, newSharedCascadeStore())
	t.Cleanup(func() { _ = installer.Close() })
	installer.fetcher = func(string) plugin.RegistryFetcher { return &fakeRegistryFetcher{artifact: raw} }
	installer.registryURL = func() (string, error) { return "https://registry.example/", nil }
	installer.registryPubKey = func() (ed25519.PublicKey, error) { return pub, nil }

	confirm := &fakeYesConfirmGate{}
	// A real Elevator over a real, but UNENROLLED, TempDir trust store: no
	// enroll() call was ever made, so GetPubKey fails closed.
	elevator := newInstallElevator(resolvePaths, clock, func(string) string { return "" })
	elevator.keystore = func(string) elevation.ElevationKeystore { return newFakeElevationKeystore(t) }

	f := install.NewFlow(install.Deps{
		Resolver: resolver.NewIntentResolver(),
		Confirm:  confirm,
		Install:  installer,
		Elevate:  elevator,
		Events:   eventBus,
		Getenv:   func(string) string { return "" },
	})

	res, err := f.Run(context.Background(), install.RunRequest{
		Intent: "grant-expand-intent",
		Index: signedTestIndex(t, []plugin.RegistryIndexEntry{{
			ID: "grant-expand-plugin", Name: "Grant Expand Plugin", Tags: []string{"grant-expand-intent"},
			LatestVersion: "1.0.0",
			Versions:      []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}},
		}}),
	})
	if err == nil {
		t.Fatal("Run: err = nil, want the unapproved-elevation refusal")
	}
	if res.Resumed {
		t.Fatal("Resumed = true after an unapproved elevation -- fail-closed contract violated")
	}
}
