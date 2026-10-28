package plugins

// Purpose (this file): IntentDispatcher's routing contract: exactly one
//   enabled match dispatches, absence and ambiguity refuse with typed errors
//   and dispatch nothing, a disabled (ungranted) registration never matches,
//   and an intent-driven install of a process-tier plugin never mints trust.
// SPORT: internal/plugins intent-dispatch (TEST) — P1-PLG-07.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

const testIntent = "demo.install"

// countingIntentHandlers records every DispatchIntent call it receives: the
// install-attempt counter the refusal tests assert stays at zero.
type countingIntentHandlers struct {
	calls     atomic.Int32
	lastName  atomic.Value
	lastInput atomic.Value
	out       []byte
}

func (h *countingIntentHandlers) DispatchTool(context.Context, string, []byte) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "no tools")
}

func (h *countingIntentHandlers) DispatchIntent(_ context.Context, name string, input []byte) ([]byte, error) {
	h.calls.Add(1)
	h.lastName.Store(name)
	h.lastInput.Store(string(input))
	return h.out, nil
}

func (h *countingIntentHandlers) RunCommand(context.Context, string, []string) error {
	return cascade.New(cascade.KindNotFound, "no commands")
}

// intentReg builds one registration declaring intents; granted=false is a
// loaded but disabled registration.
func intentReg(id string, granted bool, h plugin.BuiltinHandlers, intents ...string) plugin.BuiltinRegistration {
	specs := make([]plugin.IntentSpec, 0, len(intents))
	for _, n := range intents {
		specs = append(specs, plugin.IntentSpec{Name: n, Description: "test intent"})
	}
	reg := plugin.BuiltinRegistration{
		Manifest: plugin.Manifest{ID: id, Name: id, Provides: plugin.Provides{Intents: specs}},
		Handlers: h,
	}
	if granted {
		reg.Grants = []string{"read"}
	}
	return reg
}

// registryOf builds a loaded registry holding exactly regs.
func registryOf(regs ...plugin.BuiltinRegistration) *BuiltinRegistry {
	r := &BuiltinRegistry{entries: map[string]plugin.BuiltinRegistration{}}
	for _, reg := range regs {
		r.entries[reg.Manifest.ID] = reg
	}
	return r
}

// requireKindMsg fails unless err has kind and its message names every fragment.
func requireKindMsg(t *testing.T, err error, kind cascade.Kind, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want %v", kind)
	}
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("kind = %v (ok=%v), want %v: %v", got, ok, kind, err)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("error %q does not contain %q", err.Error(), f)
		}
	}
}

func TestIntentDispatcherSingleMatch(t *testing.T) {
	want, other := &countingIntentHandlers{out: []byte(`{"ok":true}`)}, &countingIntentHandlers{}
	d := NewIntentDispatcher(registryOf(intentReg("alpha", true, want, testIntent), intentReg("beta", true, other, "other.intent")))
	got, err := d.Dispatch(context.Background(), testIntent, []byte(`{"intent":"x"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("result = %q, want the plugin's bytes unchanged", got)
	}
	if want.calls.Load() != 1 || other.calls.Load() != 0 {
		t.Fatalf("calls alpha=%d beta=%d, want 1 and 0", want.calls.Load(), other.calls.Load())
	}
	if want.lastName.Load() != testIntent || want.lastInput.Load() != `{"intent":"x"}` {
		t.Fatalf("plugin saw name=%v input=%v", want.lastName.Load(), want.lastInput.Load())
	}
	if _, err := d.Dispatch(context.Background(), "  DEMO.Install ", nil); err != nil || want.lastName.Load() != testIntent {
		t.Fatalf("a differently spelled match reached the plugin as %v (err %v), want its declared %q", want.lastName.Load(), err, testIntent)
	}
}

func TestIntentDispatcherNoMatchNotFound(t *testing.T) {
	h := &countingIntentHandlers{}
	for name, d := range map[string]*IntentDispatcher{
		"undeclared": NewIntentDispatcher(registryOf(intentReg("alpha", true, h, "other.intent"))),
		"empty":      NewIntentDispatcher(registryOf()),
		"nil":        NewIntentDispatcher(nil),
	} {
		_, err := d.Dispatch(context.Background(), testIntent, []byte(`{}`))
		requireKindMsg(t, err, cascade.KindNotFound, testIntent, "no enabled plugin declares it")
		if err == plugin.ErrUnverifiedIndex || err == plugin.ErrIntentNotFound {
			t.Fatalf("%s: the resolver's sentinel leaked unmapped: %v", name, err)
		}
	}
	if h.calls.Load() != 0 {
		t.Fatalf("DispatchIntent called %d times, want 0", h.calls.Load())
	}
	_, err := NewIntentDispatcher(registryOf()).Dispatch(context.Background(), "  ", nil)
	requireKindMsg(t, err, cascade.KindInvalidInput, "intent is empty")
}

func TestIntentDispatcherAmbiguousConflict(t *testing.T) {
	a, b := &countingIntentHandlers{out: []byte(`{}`)}, &countingIntentHandlers{out: []byte(`{}`)}
	d := NewIntentDispatcher(registryOf(intentReg("alpha", true, a, testIntent), intentReg("beta", true, b, "DEMO.Install")))
	_, err := d.Dispatch(context.Background(), testIntent, []byte(`{}`))
	requireKindMsg(t, err, cascade.KindConflict, "alpha", "beta", "nothing was dispatched")
	if a.calls.Load() != 0 || b.calls.Load() != 0 {
		t.Fatalf("calls alpha=%d beta=%d, want 0 and 0", a.calls.Load(), b.calls.Load())
	}
}

func TestIntentDispatcherSkipsDisabled(t *testing.T) {
	enabled, disabled := &countingIntentHandlers{out: []byte(`{}`)}, &countingIntentHandlers{out: []byte(`{}`)}
	d := NewIntentDispatcher(registryOf(intentReg("alpha", false, disabled, testIntent), intentReg("beta", true, enabled, testIntent)))
	if _, err := d.Dispatch(context.Background(), testIntent, []byte(`{}`)); err != nil {
		t.Fatalf("Dispatch with one enabled match: %v", err)
	}
	if enabled.calls.Load() != 1 || disabled.calls.Load() != 0 {
		t.Fatalf("calls enabled=%d disabled=%d, want 1 and 0", enabled.calls.Load(), disabled.calls.Load())
	}
	only := NewIntentDispatcher(registryOf(intentReg("alpha", false, disabled, testIntent)))
	_, err := only.Dispatch(context.Background(), testIntent, []byte(`{}`))
	requireKindMsg(t, err, cascade.KindNotFound, testIntent)
	if disabled.calls.Load() != 0 {
		t.Fatalf("the disabled plugin was dispatched %d times", disabled.calls.Load())
	}
}

// fixedCandidateResolver answers every Resolve with one candidate.
type fixedCandidateResolver struct{ cand plugin.Candidate }

func (r fixedCandidateResolver) Resolve(context.Context, plugin.ManifestSet, *plugin.VerifiedIndex, string) ([]plugin.Candidate, error) {
	return []plugin.Candidate{r.cand}, nil
}

type yesGate struct{}

func (yesGate) Confirm(context.Context, install.Proposal) (install.ConfirmOutcome, error) {
	return install.ConfirmYes, nil
}

// approvingElevator answers with a real witness minted over a passing verifier.
type approvingElevator struct{ w install.ElevationWitness }

func (e approvingElevator) Elevate(context.Context, install.ElevationRequest) (install.ElevationResult, error) {
	return install.ElevationResult{Approved: true, Witness: e.w}, nil
}

type tallyInstaller struct {
	next  install.Installer
	calls atomic.Int32
}

func (c *tallyInstaller) Add(ctx context.Context, req install.AddRequest) (install.AddResult, error) {
	c.calls.Add(1)
	return c.next.Add(ctx, req)
}

type discardBus struct{ n atomic.Int32 }

func (b *discardBus) Publish(context.Context, install.Event) error { b.n.Add(1); return nil }

// swapInstallFlow installs f as cascade-pa's flow and restores the
// production flow this package's init builds when the test ends.
func swapInstallFlow(t *testing.T, f *install.Flow) {
	t.Helper()
	install.SetFlow(f)
	t.Cleanup(func() {
		install.SetFlow(install.NewFlow(buildInstallDeps(runtime.NewDefaultPathProvider, runtime.NewSystemClock())))
	})
}

func TestIntentInstallNeverTrustsProcessPlugin(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, processTierManifest)
	writeExecutable(t, filepath.Join(injectDir(t), exeName("process-plugin")), 0o755)
	adapter := newTestInstallerAdapter(t, "https://registry.example/", &fakeRegistryFetcher{artifact: raw}, pub)
	installer := &tallyInstaller{next: adapter}
	cand := plugin.Candidate{PluginID: "process-plugin", Name: "Process Plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "process-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}}}
	swapInstallFlow(t, install.NewFlow(install.Deps{Resolver: fixedCandidateResolver{cand: cand}, Confirm: yesGate{},
		Install: installer, Elevate: approvingElevator{w: testWitness(t, "intent-attestation-1")}, Events: &discardBus{}}))

	reg := &BuiltinRegistry{}
	_ = reg.Load()
	input, _ := json.Marshal(map[string]string{"intent": "process plugin"})
	_, err := NewIntentDispatcher(reg).Dispatch(context.Background(), install.IntentName, input)
	requireKindMsg(t, err, cascade.KindPolicyDenied, "process-plugin", "cascade plugin add")
	if n := installer.calls.Load(); n != 2 {
		t.Fatalf("installer called %d times, want 2 (decide, then the elevated retry)", n)
	}
	if _, ok, lerr := LoadMetadata(context.Background(), adapter.store, "process-plugin"); lerr != nil || ok {
		t.Fatalf("an intent-driven install left a process record: ok=%v err=%v", ok, lerr)
	}
}
