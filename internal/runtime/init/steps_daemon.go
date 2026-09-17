package init

// Purpose: wizard steps 8-9 (P1-E16-W4-S35-T6) — installing the daemon
//   as a platform service, enrolling the elevation helper, and the
//   first-run doctor summary.
// Constraints: EVERY platform decision in this file is taken against
//   Deps.GOOS, an injected value, so all three branches are unit-tested
//   on any host. The build-constrained files hold only OS-API calls and
//   no policy. Windows is tier-2 here: the step is skipped with a stated
//   reason and a nil error, never a failure — a platform that cannot
//   install a service has not had init go wrong.
// SPORT: internal/runtime/init steps 8-9 (ADD) — P1-E16-W4-S35-T6.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// GOOSWindows is the platform this wizard treats as tier-2.
const GOOSWindows = "windows"

// tier2DaemonMessage is what the wizard says on a platform where the
// daemon cannot be installed as a service, and the reason recorded in the
// journal so step 9 can repeat it rather than leaving a blank line.
const tier2DaemonMessage = "this platform has no supported service manager (tier-2); " +
	"run `cascade daemon run` yourself, or start it from Task Scheduler"

// DaemonSupported reports whether goos has a service manager this build
// installs into.
//
// Exported because it is a FACT about a platform, not a private detail of
// one step: the `--check` diff, the step itself and the step-9 summary
// must all agree about it, and three copies of one condition is how they
// stop agreeing.
func DaemonSupported(goos string) bool { return goos != GOOSWindows }

// stepDaemon is step 8: install the service, then enroll the elevation
// helper.
func (w *Wizard) stepDaemon(ctx context.Context, state *State) error {
	w.say("== daemon")
	if reason, skip := w.daemonSkipReason(state.Profile); skip {
		state.DaemonInstalled = false
		state.DaemonSkipReason = reason
		w.say("   skipped: %s", reason)
		return nil
	}
	ok, err := w.deps.Prompt.Confirm("Install the cascade daemon as a service?", w.daemonDefault(state.Profile))
	if err != nil {
		return err
	}
	if !ok {
		state.DaemonSkipReason = "declined"
		w.say("   skipped: declined")
		return nil
	}
	if !w.writing() {
		w.plan("install the cascade daemon as a platform service")
		w.plan("enroll the elevation helper's key in the daemon trust store")
		return nil
	}
	if err := w.deps.Service.Install(ctx); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: install the daemon service")
	}
	state.DaemonInstalled = true
	w.say("   installed")
	return w.enrollHelper(ctx, state)
}

// daemonSkipReason reports why step 8 installs nothing, if it does not.
//
// Three reasons, and they are different: the platform cannot, the
// operator said not to, or a worker is configured by its controller.
// Collapsing them into one boolean would make step 9's summary print the
// same sentence for all three.
func (w *Wizard) daemonSkipReason(profile string) (string, bool) {
	if w.opts.NoDaemon {
		return "--no-daemon", true
	}
	if !DaemonSupported(w.deps.GOOS) {
		return tier2DaemonMessage, true
	}
	if profile == ProfileWorker {
		return "a worker's daemon is managed by its controller", true
	}
	return "", false
}

// daemonDefault is the pre-selected answer: yes for the profiles that run
// their own daemon.
func (w *Wizard) daemonDefault(profile string) bool {
	return profile == ProfileLocal || profile == ProfileServer
}

// enrollHelper runs the elevation helper's TOFU enrollment and records
// the fingerprint step 9 prints.
//
// An enrollment failure does NOT fail init. The daemon is installed and
// the machine is usable; what the operator loses is the elevated verbs,
// and telling them that in the summary is more useful than unwinding a
// successful install over it.
func (w *Wizard) enrollHelper(ctx context.Context, state *State) error {
	if !DaemonSupported(w.deps.GOOS) {
		w.say("   helper enrollment skipped: %s", tier2DaemonMessage)
		return nil
	}
	fingerprint, err := w.deps.Enroller.Enroll(ctx)
	if err != nil {
		w.say("   helper enrollment failed: %v", err)
		w.say("   elevated verbs will refuse until `cascade elevate-helper --enroll` succeeds")
		return nil
	}
	state.HelperFingerprint = fingerprint
	w.say("   helper enrolled: %s", fingerprint)
	return nil
}

// stepDoctor is step 9: run the first-run health check and render the
// summary card.
//
// A doctor that reports problems does not fail init either. Init's job is
// to set the machine up; doctor's is to say what is still wrong with it,
// and an operator who can read that list is better served than one whose
// setup exited non-zero at the last step.
func (w *Wizard) stepDoctor(ctx context.Context, state *State) error {
	w.say("== doctor")
	if !w.writing() {
		w.plan("run `cascade doctor --first-run`")
		w.summary(*state)
		return nil
	}
	summary, err := w.deps.Doctor.FirstRun(ctx)
	if err != nil {
		w.say("   the first-run health check could not complete: %v", err)
	} else if summary != "" {
		w.say("%s", summary)
	}
	w.summary(*state)
	return nil
}

// summary renders the step-9 card: what this run set up, and what to do
// next.
func (w *Wizard) summary(state State) {
	w.say("")
	w.say("== cascade is set up")
	w.say("   profile     %s", orNone(state.Profile))
	w.say("   storage     %s", orNone(state.StoragePath))
	w.say("   plugins     %s", orNone(joinRefs(state.Plugins)))
	w.say("   providers   %s", orNone(joinRefs(state.Providers)))
	w.say("   harnesses   %s", harnessLine(state))
	w.say("   telemetry   %s", onOff(state.Telemetry))
	w.say("   daemon      %s", daemonLine(state))
	if state.HelperFingerprint != "" {
		w.say("   helper key  %s", state.HelperFingerprint)
	}
	w.say("")
	w.say("next: `cascade doctor` for a full health check, `cascade context sync` after editing instructions")
}

// harnessLine renders step 6's outcome, including why nothing happened.
//
// "none" and "this platform cannot look" are different answers, and a
// summary that printed the first for the second would tell a windows
// operator they have no harnesses installed when nobody checked.
func harnessLine(state State) string {
	if len(state.Harnesses) > 0 {
		return joinRefs(state.Harnesses)
	}
	if state.HarnessSkipReason != "" {
		return "none (" + state.HarnessSkipReason + ")"
	}
	return "none"
}

// daemonLine renders step 8's outcome, including why nothing happened.
func daemonLine(state State) string {
	if state.DaemonInstalled {
		return "installed"
	}
	if state.DaemonSkipReason != "" {
		return "not installed (" + state.DaemonSkipReason + ")"
	}
	return "not installed"
}

// orNone renders an empty value as a word rather than as a blank.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// onOff renders a bool the way the summary reads it.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
