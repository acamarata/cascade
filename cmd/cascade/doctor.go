// Purpose: `cascade doctor` and `cascade doctor bundle` (07-CLI-COMMAND-TREE
//
//	§doctor), the diagnostic surface over internal/doctor. This file is CLI
//	wiring and composition only: the check registry, the flag parsing, the
//	render dispatch, and the outcome-to-process-result translation. No check
//	logic is implemented here; every probe is a doctor.Check the owning
//	subsystem supplies.
//
// Inputs: cobra args/flags; a doctorDeps injected at construction so no test
//
//	touches the real environment or the real clock (Art.7.1).
//
// Outputs: process output via internal/output.Writer, the human table by
//
//	default and the versioned envelope under --json; a typed taxonomy error
//	when the run's outcome is not ok.
//
// Constraints: never writes to os.Stdout/os.Stderr directly (internal/output
//
//	owns those). Every string that reaches the user or the bundle passes
//	through doctor.RedactLines first: a check message can legitimately echo
//	back an offending config value, and doctor output is the most commonly
//	pasted-into-a-bug-report text this binary produces.
//
// SPORT: cmd/cascade/doctor (ADD) - doctor command, bundle subcommand.
package main

import (
	"bytes"
	"context"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// doctorDeps carries every external input the doctor command tree needs,
// mirroring statusDeps's established injection pattern.
type doctorDeps struct {
	// Registry builds a fresh check registry per invocation. A function,
	// not a value: doctor.CheckRegistry.Register panics on a duplicate name,
	// so one shared registry rebuilt across two command executions in a
	// single process (which every test binary does) would panic the second
	// time.
	Registry func() *doctor.CheckRegistry
	Paths    runtime.PathProvider
	Getenv   runtime.Getenv
	Environ  func() []string
	Clock    runtime.Clock
	// BundleDir is where `doctor bundle` writes; "" lets internal/doctor
	// apply its own os.TempDir default. Tests set t.TempDir().
	BundleDir string
}

// productionDoctorDeps builds doctorDeps against the real environment.
func productionDoctorDeps() doctorDeps {
	return doctorDeps{
		Registry: productionCheckRegistry,
		Paths:    lazyPaths{},
		Getenv:   os.Getenv,
		Environ:  os.Environ,
		Clock:    runtime.SystemClock{},
	}
}

// productionCheckRegistry is the composition root for doctor's checks: the
// one place a subsystem's Check is mounted, with no init() side effects.
//
// Only checks with a real data source are registered. doctor ships two
// further framework checks, mcp_integration and subsystem_census, whose
// constructors take a HarnessDiscoverer and a SubsystemStateProvider; neither
// interface has a production implementation anywhere in this tree, and
// satisfying one with a hand-written stand-in here would put a check in the
// report that probes nothing (Art.1). They stay unmounted, and visibly so,
// until their providers land.
func productionCheckRegistry() *doctor.CheckRegistry {
	reg := doctor.NewCheckRegistry()
	reg.Register(doctor.NewDoctorSelfCheck())
	return reg
}

// mountDoctorCmd attaches the top-level `doctor` command, following
// mountStatusCmd's exact pattern.
func mountDoctorCmd(root *cobra.Command) {
	cmd := newDoctorCmd(productionDoctorDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// doctorFlags holds the doctor command's own (non-persistent) flags. One
// value per constructed command, so two trees in one test binary do not share
// flag state.
type doctorFlags struct {
	firstRun bool
	fix      bool
}

// newDoctorCmd builds the `doctor` command and its `bundle` subcommand.
func newDoctorCmd(deps doctorDeps) *cobra.Command {
	f := &doctorFlags{}
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the local cascade installation",
		Long: "Run every registered diagnostic check against this installation and\n" +
			"report the results. Exits non-zero when any check reports a warning\n" +
			"or an error, so it can gate a script or a CI step.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd, deps, f)
		},
	}
	cmd.Flags().BoolVar(&f.firstRun, "first-run", false,
		"run only the checks tagged for a first-run health check")
	cmd.Flags().BoolVar(&f.fix, "fix", false,
		"attempt to remediate every fixable check that reports a problem")
	cmd.AddCommand(newDoctorBundleCmd(deps))
	return cmd
}

// runDoctor executes the registered checks, renders the report, and returns
// the run's outcome as a process result.
func runDoctor(cmd *cobra.Command, deps doctorDeps, f *doctorFlags) error {
	report, err := executeChecks(cmd.Context(), deps, f)
	if err != nil {
		return err
	}
	view, err := doctorViewFor(cmd, deps, report)
	if err != nil {
		return err
	}
	if err := doctorOutputWriter(cmd).Result(view); err != nil {
		return err
	}
	return doctorOutcomeError(report)
}

// executeChecks resolves the check set from the flags and runs it.
func executeChecks(ctx context.Context, deps doctorDeps, f *doctorFlags) (doctor.RunReport, error) {
	reg := deps.Registry()
	checks := reg.List()
	if f.firstRun {
		checks = reg.FirstRun()
	}
	if len(checks) == 0 {
		// An empty run reports OutcomeOK, which would render as a silent
		// clean bill of health for a doctor that examined nothing. Absence
		// is never a pass (Art.1).
		return doctor.RunReport{}, cascade.New(cascade.KindNotFound,
			"cascade doctor: no diagnostic checks are registered for this run")
	}
	return doctor.Run(ctx, checks, doctor.RunOptions{
		FirstRunOnly: f.firstRun,
		Fix:          f.fix,
		Clock:        deps.Clock,
	}), nil
}

// redactDoctorReport runs every human-authored string in the report through
// doctor.RedactLines. Each of these fields is a one-liner by contract (see
// CheckResult's field docs), which is exactly the single-line-at-a-time
// stream RedactLines documents itself for.
func redactDoctorReport(report doctor.RunReport) doctor.RunReport {
	out := doctor.RunReport{
		Entries:     make([]doctor.ReportEntry, len(report.Entries)),
		GeneratedAt: report.GeneratedAt,
	}
	for i, e := range report.Entries {
		red := doctor.RedactLines([]string{
			e.Result.Message, e.Result.Detail, e.Result.Remediation, e.FixedBy.Delta,
		})
		out.Entries[i] = doctor.ReportEntry{
			Name: e.Name,
			Result: doctor.CheckResult{
				Status: e.Result.Status, Message: red[0], Detail: red[1], Remediation: red[2],
			},
			Fixed:   e.Fixed,
			FixedBy: doctor.FixResult{Applied: e.FixedBy.Applied, Delta: red[3]},
		}
	}
	return out
}

// doctorView carries the redacted report to internal/output: the embedded
// RunReport supplies the --json envelope's data shape, String() supplies the
// human rendering, exactly as statusHumanView does for `cascade status`.
type doctorView struct {
	doctor.RunReport
	text string
}

// String renders the human form built by doctorViewFor.
func (v doctorView) String() string { return v.text }

// doctorViewFor redacts the report and renders its human form through
// doctor.Render, resolving colour with doctor.UseColor against the real
// output stream. cmd.OutOrStdout() is the process's stdout unless a test
// replaced it, so the type assertion reaches the real terminal without this
// file naming os.Stdout (R-14.137's output gate); a non-file writer yields a
// nil *os.File, which UseColor already treats as "not a terminal".
func doctorViewFor(cmd *cobra.Command, deps doctorDeps, report doctor.RunReport) (doctorView, error) {
	red := redactDoctorReport(report)
	noColor, _ := cmd.Flags().GetBool("no-color")
	outFile, _ := cmd.OutOrStdout().(*os.File)
	useColor := doctor.UseColor(outFile, deps.Getenv("NO_COLOR"), noColor)

	var buf bytes.Buffer
	// jsonOut is false: the --json contract in this binary is
	// internal/output's versioned envelope (D/S-06.T5), reached through
	// Result below, not doctor's own minimal {version, data} shape. Render's
	// third parameter selects the coloured renderer, which is exactly the
	// question UseColor answers.
	if err := doctor.Render(&buf, red, false, useColor); err != nil {
		return doctorView{}, cascade.Wrap(cascade.KindInternal, err, "cascade doctor: render report")
	}
	return doctorView{RunReport: red, text: strings.TrimRight(buf.String(), "\n")}, nil
}

// doctorOutcomeError translates the run's verdict into a process result.
//
// WHICH outcomes fail is internal/doctor's decision, taken here through
// DefaultOutcomeExitCode over RunReport.Outcome(). Only the translation into
// a taxonomy error happens at this boundary, and it has to: this binary
// derives its exit status from an error's Kind (main.go), and doctor's own
// placeholder integers collide with the frozen table (its 1 is ExitInternal,
// its 2 is ExitInvalidInput). See the CONTRADICTION note in this defect's
// journal.
func doctorOutcomeError(report doctor.RunReport) error {
	outcome := report.Outcome()
	if doctor.DefaultOutcomeExitCode(outcome) == cascade.ExitOK {
		return nil
	}
	return cascade.Newf(cascade.KindUnavailable,
		"cascade doctor: %d check(s) reported a problem (outcome %s)",
		countNotOK(report), outcome)
}

// countNotOK counts entries that did not report ok.
func countNotOK(report doctor.RunReport) int {
	n := 0
	for _, e := range report.Entries {
		if e.Result.Status != doctor.StatusOK {
			n++
		}
	}
	return n
}

// doctorOutputWriter mirrors statusOutputWriter's local convention.
func doctorOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
