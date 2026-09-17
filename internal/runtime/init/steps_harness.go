package init

// Purpose: wizard steps 6-7 (P1-E16-W4-S35-T6) — harness detection and
//   wiring, and the telemetry opt-in.
// Constraints: detection CONSUMES the S-35.T3 detector; this package
//   contains no path probing of its own and no fourth harness kind. The
//   --harness filter narrows the DETECTED set and never adds to it: a
//   harness that is not installed cannot be wired, and pretending
//   otherwise would write instruction files nothing reads.
// SPORT: internal/runtime/init steps 6-7 (ADD) — P1-E16-W4-S35-T6.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stepHarnesses is step 6.
func (w *Wizard) stepHarnesses(ctx context.Context, state *State) error {
	w.say("== harnesses")
	found, err := w.deps.Detector.Detect(ctx)
	if err != nil {
		// A platform whose harness paths this build does not resolve
		// REFUSES rather than reporting an empty fleet — reporting "no
		// harnesses installed" for a machine nobody looked at is a false
		// negative the operator would act on.
		//
		// But that refusal must not fail the whole setup. Windows is a
		// tier-2 platform where a headless one-shot is exactly what the
		// tier promises, and a step that does not apply there is the
		// same situation step 8 is in with the service manager: skipped
		// with a stated reason, and a nil error. Any OTHER failure — an
		// unreadable home, a cancelled context — still fails, because
		// those are not "this step does not apply here".
		//
		// Found by the windows CI lane. Every local run on darwin took
		// the other branch, and `cascade init` on windows failed at
		// step 6 for as long as this returned the error.
		if cascade.HasKind(err, cascade.KindUnsupported) {
			state.HarnessSkipReason = err.Error()
			w.say("   skipped: %v", err)
			return nil
		}
		return err
	}
	wanted, err := w.harnessFilter(found)
	if err != nil {
		return err
	}

	wired := make([]string, 0, len(found))
	for _, h := range found {
		if !h.Detected {
			w.say("   %-10s not installed (looked in %s)", h.Kind, h.InstallPath)
			continue
		}
		if !wanted[h.Kind] {
			w.say("   %-10s installed, skipped by --harness", h.Kind)
			continue
		}
		ok, confirmErr := w.deps.Prompt.Confirm("Wire "+h.Kind+"?", true)
		if confirmErr != nil {
			return confirmErr
		}
		if !ok {
			w.say("   %-10s installed, declined", h.Kind)
			continue
		}
		if !w.writing() {
			w.plan("wire %s: instruction files, hook pack, MCP entry", h.Kind)
			wired = append(wired, h.Kind)
			continue
		}
		if wireErr := w.deps.Wirer.Wire(ctx, h.Kind, w.deps.Cwd); wireErr != nil {
			return cascade.Wrapf(cascade.KindUnavailable, wireErr, "cascade init: wiring %s", h.Kind)
		}
		wired = append(wired, h.Kind)
		w.say("   %-10s wired", h.Kind)
	}
	state.Harnesses = wired
	return nil
}

// harnessFilter resolves --harness against what was actually detected.
//
// A name in --harness that no detector reported is a REFUSAL, not a
// silent no-op: the operator named a harness expecting it to be set up,
// and a run that finished successfully having wired nothing would be
// exactly the wrong answer.
func (w *Wizard) harnessFilter(found []HarnessState) (map[string]bool, error) {
	known := map[string]bool{}
	for _, h := range found {
		known[h.Kind] = true
	}
	if len(w.opts.Harnesses) == 0 {
		return known, nil
	}
	wanted := map[string]bool{}
	for _, name := range w.opts.Harnesses {
		if !known[name] {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"cascade init: --harness names %q, which is not a harness this build knows", name)
		}
		wanted[name] = true
	}
	return wanted, nil
}

// stepTelemetry is step 7. The default is No, and it is No in every
// non-interactive mode too: a mode that answers for the operator must not
// answer yes to the one question that shares their data.
func (w *Wizard) stepTelemetry(_ context.Context, state *State) error {
	w.say("== telemetry")
	on, err := w.deps.Prompt.Confirm("Send anonymous usage telemetry?", false)
	if err != nil {
		return err
	}
	state.Telemetry = on
	if on {
		w.say("   enabled")
	} else {
		w.say("   off")
	}
	return nil
}
