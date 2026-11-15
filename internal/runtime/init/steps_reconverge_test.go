package init

// Purpose: the --reconverge run's discovery half, end to end through
//   Wizard.Run over the recorded collaborators and an in-memory store.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
)

// discoveryReconverge builds a --reconverge wizard whose setup file names
// a root, a node and the strict preset, over an empty configured machine.
func discoveryReconverge(t *testing.T, mode Mode, store DiscoveryStore) (*Wizard, *recorder) {
	t.Helper()
	w, rec, _, _ := fixture(t, Options{Mode: mode, Reconverge: true}, nil)
	w.deps.Current = fakeCurrent{view: fakeConfigView{values: map[string]any{}}}
	w.deps.Discovery = store
	w.opts.Spec = &initconfig.Spec{
		Roots:  initconfig.RootsConfig{Paths: []string{"/r/new"}},
		Nodes:  initconfig.NodesConfig{Pair: []string{"n1.invalid"}},
		Policy: initconfig.PolicyConfig{Preset: "strict"},
	}
	return w, rec
}

// TestReconvergeDiscoveryStepRefusesWithoutAStore: a setup file naming
// the discovery tables on a build with no store is refused before any
// converge runs; one naming only providers needs no store.
func TestReconvergeDiscoveryStepRefusesWithoutAStore(t *testing.T) {
	w, rec := discoveryReconverge(t, ModeYes, nil)
	_, err := w.Run(context.Background())
	if !errors.Is(err, ErrDiscoveryStoreMissing) || !strings.Contains(fmtErr(err), "no discovery store") {
		t.Fatalf("err = %v, want ErrDiscoveryStoreMissing", err)
	}
	if len(rec.subArgs) != 0 {
		t.Errorf("the refused run still converged: %v", rec.subArgs)
	}
	w, _ = discoveryReconverge(t, ModeYes, nil)
	w.opts.Spec = &initconfig.Spec{}
	w.opts.ReconvergeOptions = ReconvergeOptions{Providers: []string{"anthropic"}}
	report, err := w.Run(context.Background())
	if err != nil || report.Discovery != nil {
		t.Errorf("a providers-only reconverge: err %v, discovery %+v; want no refusal and no delta", err, report.Discovery)
	}
}

// TestReconvergeDiscoveryStepReplayIsEmpty: the first reconverge adds the
// root, the node and the preset; an identical second one writes nothing.
func TestReconvergeDiscoveryStepReplayIsEmpty(t *testing.T) {
	store := &fakeDiscoveryStore{have: DiscoveryState{Policy: "balanced"}}
	w, _ := discoveryReconverge(t, ModeYes, store)
	report, err := w.Run(context.Background())
	if err != nil || report.Discovery == nil {
		t.Fatalf("first reconverge: %v, delta %v", err, report.Discovery)
	}
	got := report.Discovery
	if strings.Join(got.Roots, ",") != "/r/new" || strings.Join(got.Nodes, ",") != "n1.invalid" || got.Policy != "strict" {
		t.Errorf("first delta = %+v, want the root, the node and strict", *got)
	}
	writes := len(store.writes)
	w, _ = discoveryReconverge(t, ModeYes, store)
	report, err = w.Run(context.Background())
	if err != nil || report.Discovery == nil || !report.Discovery.Empty() {
		t.Fatalf("replay: %v, delta %+v; want an empty delta", err, report.Discovery)
	}
	if len(store.writes) != writes {
		t.Errorf("the replay wrote %v", store.writes[writes:])
	}
}

// TestReconvergeDiscoveryStepCheckWritesNothing: --check plans every
// write a real run would make and makes none.
func TestReconvergeDiscoveryStepCheckWritesNothing(t *testing.T) {
	store := &fakeDiscoveryStore{}
	w, _ := discoveryReconverge(t, ModeCheck, store)
	report, err := w.Run(context.Background())
	if err != nil || report.Discovery == nil || report.Discovery.Empty() {
		t.Fatalf("check: %v, delta %+v; want the planned delta", err, report.Discovery)
	}
	if len(store.writes) != 0 {
		t.Errorf("--check wrote %v", store.writes)
	}
	diff := strings.Join(report.Diff, "\n")
	for _, want := range []string{"add root /r/new", "add node n1.invalid", "set policy preset strict"} {
		if !strings.Contains(diff, want) {
			t.Errorf("the --check diff lacks %q:\n%s", want, diff)
		}
	}
}

// fmtErr renders err, or "" for nil.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
