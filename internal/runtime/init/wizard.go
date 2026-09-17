package init

// Purpose: the wizard itself (P1-E16-W4-S35-T6) — the step loop, the
//   seams every step is driven through, and the three run modes
//   (interactive, --yes, --check).
// Inputs: Deps, wired by cmd/cascade to the REAL provider intake, harness
//   detector, adapters, service installer and doctor. Nothing in this
//   package substitutes a stand-in for any of them (Art.1).
// Outputs: a Report, and a journal entry after every completed step.
// Constraints: the platform BRANCH is a seam (Deps.GOOS), not a build
//   constraint, so every platform's decision path is unit-testable on any
//   host; only the OS-API calls that cannot compile cross-platform live
//   behind build constraints, and those carry no policy.
// SPORT: internal/runtime/init wizard (ADD) — P1-E16-W4-S35-T6.

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Mode selects how the wizard answers its own questions.
type Mode uint8

const (
	// ModeInteractive prompts on a TTY. The default.
	ModeInteractive Mode = iota
	// ModeYes accepts every default with zero prompts (--yes). This is
	// the automation path, and the one T5's acceptance harness drives.
	ModeYes
	// ModeCheck renders the diff a run WOULD produce and writes
	// nothing at all — not the journal, not config, not a harness file
	// (--check).
	ModeCheck
)

// Options are the run's flags.
type Options struct {
	Mode Mode
	// Profile preselects step 2 (--profile).
	Profile string
	// Harnesses filters step 6 to this subset of the DETECTED set
	// (--harness). Empty means every detected harness.
	Harnesses []string
	// NoDaemon skips step 8 (--no-daemon).
	NoDaemon bool
	// Reconverge is step 1's third branch. T6 delegates it: the full
	// three-way merge is P/S-35.T7's scope (R-14.52), and this build
	// refuses rather than performing a partial version of it.
	Reconverge bool
}

// Report is what a run produces.
type Report struct {
	// State is the journal as it stands at the end of the run.
	State State
	// Resumed reports whether this run picked up an existing journal.
	Resumed bool
	// Steps names the steps this run actually performed, which is what
	// a resumed run's delta report prints.
	Steps []string
	// Diff is --check's rendering of what a real run would change.
	Diff []string
}

// ErrReconvergeNotImplemented is step 1's refusal for the reconverge
// branch. It is a REFUSAL, not a fallback to a fresh run: a fresh run
// over a configured machine would overwrite the configuration the
// operator asked to converge.
var ErrReconvergeNotImplemented = cascade.New(cascade.KindUnsupported,
	"cascade init: reconverge is not available in this build (P1-E16-W4-S35-T7); "+
		"re-run without --reconverge to resume, or remove ~/.cascade to start fresh")

// Wizard runs the nine steps.
type Wizard struct {
	deps Deps
	opts Options
	// diff accumulates what a --check run would change.
	diff []string
}

// New builds a wizard over deps and opts.
func New(deps Deps, opts Options) *Wizard { return &Wizard{deps: deps, opts: opts} }

// stepFunc is one step's implementation, mutating the journal in place.
type stepFunc func(w *Wizard, ctx context.Context, state *State) error

// steps maps each step to its implementation. A map rather than a switch
// so Run cannot silently skip a step the enum has and the switch forgot.
func steps() map[Step]stepFunc {
	return map[Step]stepFunc{
		StepPreflight: (*Wizard).stepPreflight,
		StepProfile:   (*Wizard).stepProfile,
		StepStorage:   (*Wizard).stepStorage,
		StepPlugins:   (*Wizard).stepPlugins,
		StepProviders: (*Wizard).stepProviders,
		StepHarnesses: (*Wizard).stepHarnesses,
		StepTelemetry: (*Wizard).stepTelemetry,
		StepDaemon:    (*Wizard).stepDaemon,
		StepDoctor:    (*Wizard).stepDoctor,
	}
}

// Run executes every step that has not already completed.
//
// The journal is written after EACH step, not at the end: the whole
// contract is that a kill between two steps loses nothing, and a journal
// written once at the end would satisfy it only for runs that do not
// need it.
func (w *Wizard) Run(ctx context.Context) (Report, error) {
	if err := w.validate(); err != nil {
		return Report{}, err
	}
	state, found, err := LoadState(w.deps.Home)
	if err != nil {
		return Report{}, err
	}
	if w.opts.Reconverge {
		return Report{}, ErrReconvergeNotImplemented
	}
	report := Report{Resumed: found && state.CompletedStep > 0}

	table := steps()
	for step := state.NextStep(); step <= LastStep; step++ {
		run, ok := table[step]
		if !ok {
			return report, cascade.Newf(cascade.KindInternal, "cascade init: step %d has no implementation", int(step))
		}
		if err := run(w, ctx, &state); err != nil {
			report.State = state
			return report, err
		}
		state.CompletedStep = step
		report.Steps = append(report.Steps, step.String())
		if w.opts.Mode == ModeCheck {
			continue
		}
		if err := SaveState(w.deps.Home, state); err != nil {
			report.State = state
			return report, err
		}
		if step == StepProfile && state.Profile == ProfileWorker {
			// The worker profile handed off to `cascade node enroll`;
			// there is nothing further for this wizard to set up, and
			// running the remaining steps would configure a machine
			// that is about to be configured by the controller.
			break
		}
	}
	report.State = state
	report.Diff = w.diff
	if w.opts.Mode != ModeCheck && state.Complete() {
		// A completed run leaves no journal: its presence is the signal
		// that a run was interrupted, so leaving one behind would make
		// the next run report a resume that has nothing to resume.
		if err := DeleteState(w.deps.Home); err != nil {
			return report, err
		}
	}
	return report, nil
}

// validate refuses a half-wired wizard before it changes anything.
func (w *Wizard) validate() error {
	missing := []string{}
	for name, present := range map[string]bool{
		"Home": w.deps.Home != "", "Out": w.deps.Out != nil, "Prompt": w.deps.Prompt != nil,
		"Detector": w.deps.Detector != nil, "Wirer": w.deps.Wirer != nil,
		"Catalog": w.deps.Catalog != nil, "Service": w.deps.Service != nil, "Enroller": w.deps.Enroller != nil,
		"Doctor": w.deps.Doctor != nil, "Sub": w.deps.Sub != nil,
		"Storage": w.deps.Storage != nil, "Secrets": w.deps.Secrets != nil,
		"GOOS": w.deps.GOOS != "",
	} {
		if !present {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sortStrings(missing)
	return cascade.Newf(cascade.KindInternal,
		"cascade init: the wizard was built without %v; a step with no collaborator would be skipped silently", missing)
}

// say renders one line of the wizard's own output.
func (w *Wizard) say(format string, args ...any) {
	_, _ = fmt.Fprintf(w.deps.Out, format+"\n", args...)
}

// plan records what a --check run would have done, and says it.
//
// Both, deliberately: the diff slice is what a caller inspects, and the
// line is what an operator reads. A --check that only returned a
// structure would print nothing on a terminal.
func (w *Wizard) plan(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	w.diff = append(w.diff, line)
	w.say("would %s", line)
}

// interactive reports whether a real operator is answering. Both --yes
// and --check answer for them, and the two steps that need an answer
// nobody can default (a provider credential, a server connection
// reference) ask this rather than naming one mode.
func (w *Wizard) interactive() bool { return w.opts.Mode == ModeInteractive }

// writing reports whether this run may change anything. Every step asks
// before acting, which is what makes --check a mode rather than a flag
// each step has to remember.
func (w *Wizard) writing() bool { return w.opts.Mode != ModeCheck }

// sortStrings is a tiny local sort so validate's message is stable.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
