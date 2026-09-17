package init

// Purpose (this file): the reconverge APPLY path — that what
//   reportConvergence printed is what applyConvergence then does.
//   Split from reconverge_test.go, which holds the pure decision
//   functions, for Art.10.3's 300-line cap.
// SPORT: internal/runtime/init reconverge apply (ADD) — P1-E16-W4-S35-T14.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedSub fails the invocations whose first two words match a key,
// and records every call. A per-command error, rather than the recorder's
// all-or-nothing subErr, because the point of the non-fatal test is that
// ONE failing toggle does not stop the rest.
type scriptedSub struct {
	calls [][]string
	fail  map[string]error
}

func (s *scriptedSub) Run(_ context.Context, args ...string) error {
	s.calls = append(s.calls, args)
	if len(args) >= 2 {
		if err, ok := s.fail[args[0]+" "+args[1]]; ok {
			return err
		}
	}
	return nil
}

// reconvergeWizard builds a writing reconverge over a fixed machine.
func reconvergeWizard(t *testing.T, opts ReconvergeOptions, cur CurrentState) (*Wizard, *scriptedSub) {
	t.Helper()
	w, _, _, _ := fixture(t, Options{Mode: ModeYes, Reconverge: true, ReconvergeOptions: opts}, nil)
	sub := &scriptedSub{fail: map[string]error{}}
	w.deps.Current = cur
	w.deps.Sub = sub
	return w, sub
}

// calledWith reports whether argv was run, joined for a readable failure.
func calledWith(sub *scriptedSub, want string) bool {
	for _, call := range sub.calls {
		if strings.Join(call, " ") == want {
			return true
		}
	}
	return false
}

// TestPluginTogglesAreAppliedNotOnlyPrinted is the rule this whole path
// exists for: the convergence was computed, printed and then dropped, so
// `--enable-plugin` / `--disable-plugin` announced a plan and performed
// none of it. A run that prints "enable plugin pbd" and leaves it off is
// worse than one that prints nothing — the operator stops checking.
func TestPluginTogglesAreAppliedNotOnlyPrinted(t *testing.T) {
	w, sub := reconvergeWizard(t,
		ReconvergeOptions{EnablePlugins: []string{"pbd"}, DisablePlugins: []string{"legacy"}},
		fakeCurrent{view: fakeConfigView{}, enabled: []string{"legacy"}},
	)

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !calledWith(sub, "plugin enable pbd") {
		t.Errorf("`cascade plugin enable pbd` was never run; calls were %v", sub.calls)
	}
	if !calledWith(sub, "plugin disable legacy") {
		t.Errorf("`cascade plugin disable legacy` was never run; calls were %v", sub.calls)
	}
}

// TestACheckRunTogglesNothing keeps --check honest: it prints the same
// plan and touches nothing, which is the only reason anyone trusts it.
func TestACheckRunTogglesNothing(t *testing.T) {
	w, sub := reconvergeWizard(t,
		ReconvergeOptions{EnablePlugins: []string{"pbd"}},
		fakeCurrent{view: fakeConfigView{}},
	)
	w.opts.Mode = ModeCheck

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run(--check): %v", err)
	}
	if len(sub.calls) != 0 {
		t.Errorf("a --check run executed %v", sub.calls)
	}
}

// TestOneFailingToggleDoesNotFailTheConverge: a plugin that will not turn
// off is worth telling the operator about, and is not a reason to report
// total failure for a run that wrote the config and regenerated the
// harness files. The provider re-verify above it makes the same trade.
func TestOneFailingToggleDoesNotFailTheConverge(t *testing.T) {
	w, sub := reconvergeWizard(t,
		ReconvergeOptions{DisablePlugins: []string{"pbd"}, EnablePlugins: []string{"alpha"}},
		fakeCurrent{view: fakeConfigView{}, enabled: []string{"pbd"}},
	)
	sub.fail["plugin disable"] = errors.New("pbd is built into this binary")

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatalf("one refused toggle failed the whole converge: %v", err)
	}
	if !calledWith(sub, "plugin enable alpha") {
		t.Errorf("the refusal stopped the toggles after it; calls were %v", sub.calls)
	}
}
