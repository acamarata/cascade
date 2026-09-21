package main

// Purpose: `cascade init` (P1-E16-W4-S35-T6) — the command, and the
//   composition root that hands the wizard the REAL harness detector,
//   adapters, plugin catalog, service installer, helper enroller and
//   doctor.
// Inputs: the flags 07 ratifies (--yes, --check, --profile, --harness,
//   --no-daemon) and the real environment.
// Outputs: the wizard's own rendering, plus exit 3 when --check finds
//   work to do.
// Constraints: every collaborator below is the shipped implementation.
//   The wizard refuses to run at all if one is missing, so there is no
//   path on which a step is quietly skipped (Art.1).
// SPORT: cmd/cascade init (ADD) — P1-E16-W4-S35-T6.

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/runtime"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"

	"github.com/spf13/cobra"
)

// initFlags holds the command's own flags, one value per constructed
// command so two trees in one test binary do not share flag state.
type initFlags struct {
	yes        bool
	check      bool
	profile    string
	harnesses  []string
	noDaemon   bool
	reconverge bool
	configPath string
	force      []string
}

// mountInitCmd attaches `cascade init`, following mountDoctorCmd's
// pattern.
func mountInitCmd(root *cobra.Command) {
	root.AddCommand(newInitCmd())
}

// newInitCmd builds the command.
func newInitCmd() *cobra.Command {
	f := &initFlags{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set cascade up on this machine",
		Long: "Walks the nine setup steps: preflight, profile, storage, plugins,\n" +
			"providers, harnesses, telemetry, daemon, and doctor: a first-run\n" +
			"health check.\n\n" +
			"Every completed step is journaled, so a run that is killed part way\n" +
			"resumes at the step that did not finish rather than asking again.\n\n" +
			"--yes accepts every default and asks nothing, which is the path\n" +
			"automation uses. --check reports what a run would change and writes\n" +
			"nothing at all, exiting 3 when there is work to do.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "accept every default and ask nothing")
	cmd.Flags().BoolVar(&f.check, "check", false, "report what a run would change, and write nothing")
	cmd.Flags().StringVar(&f.profile, "profile", "", "preselect the profile (local, server or worker)")
	cmd.Flags().StringSliceVar(&f.harnesses, "harness", nil,
		"wire only these harnesses, from the ones detected")
	cmd.Flags().StringVar(&f.configPath, "config", "",
		"read the answers from a cascade.init/v1 setup file instead of prompting")
	cmd.Flags().BoolVar(&f.noDaemon, "no-daemon", false, "skip installing the daemon as a service")
	cmd.Flags().StringSliceVar(&f.force, "force-section", nil,
		"overwrite your edits in these config sections during a reconverge")
	cmd.Flags().BoolVar(&f.reconverge, "reconverge", false,
		"converge an existing installation toward the setup file, keeping your own edits")
	return cmd
}

// ErrInitCheckFoundWork is `--check`'s outcome when the machine is not
// fully set up. It is a distinct ANSWER, not a failure in the wizard.
//
// KindNotFound, which the taxonomy maps to exit 3, is what 08 §2's
// exit-code table ratifies for this flag. Worth recording: `cascade
// context sync --check` answers the same shape of question — "there is
// work to do" — with KindConflict, exit 4. Two --check flags on one CLI
// exiting differently for the same kind of answer is a real
// inconsistency; reconciling it means changing a landed exit code that
// scripts may already branch on, so it is recorded in R-14.255 rather
// than changed here on the way past.
var ErrInitCheckFoundWork = cascade.New(cascade.KindNotFound,
	"cascade init --check: this machine is not fully set up")

// ErrReconvergeCheckFoundWork is the same answer for a --reconverge
// --check run. A separate error because it is a different statement: the
// machine IS set up, and has drifted from the setup file.
var ErrReconvergeCheckFoundWork = cascade.New(cascade.KindNotFound,
	"cascade init --reconverge --check: this machine has drifted from the setup file")

// runInit resolves the environment, builds the wizard and runs it.
func runInit(cmd *cobra.Command, f *initFlags) error {
	// The setup file is read FIRST. A file that cannot be read, or that
	// carries a credential, or that names a browser flow, fails here —
	// before the wizard exists, and therefore before any step could have
	// changed the machine.
	spec, err := resolveInitSpec(f)
	if err != nil {
		return err
	}
	paths, err := runtime.NewPathProvider(nil, nil)
	if err != nil {
		return err
	}
	deps, err := productionInitDeps(cmd, paths)
	if err != nil {
		return err
	}
	opts := initOptions(f)
	opts.Spec = spec
	if spec != nil {
		deps.Prompt = cascadeinit.NewSpecPrompter(spec)
	}
	if f.reconverge {
		deps.Current = initCurrentState{paths: paths}
		if opts.ReconvergeOptions, err = reconvergeOptionsFrom(spec, f.force); err != nil {
			return err
		}
	}
	report, err := cascadeinit.New(deps, opts).Run(cmd.Context())
	if err != nil {
		return err
	}
	if f.check && len(report.Diff) > 0 {
		if f.reconverge {
			return ErrReconvergeCheckFoundWork
		}
		return ErrInitCheckFoundWork
	}
	return nil
}

// resolveInitSpec reads the setup file and merges it with the flags and
// the environment, or returns nil when this run is fully interactive.
//
// A spec is produced whenever ANY non-interactive input exists — a
// --config path, CASCADE_INIT_CONFIG, or CASCADE_NO_INPUT — because the
// guard that refuses an unanswerable prompt lives in the spec's prompter,
// and a NO_INPUT run with no file still has to be guarded.
func resolveInitSpec(f *initFlags) (*initconfig.Spec, error) {
	path := f.configPath
	if path == "" {
		path = os.Getenv(initconfig.EnvConfig)
	}
	if path == "" && os.Getenv(initconfig.EnvNoInput) == "" {
		return nil, nil
	}
	var file *initconfig.InitConfig
	if path != "" {
		scanner, err := initconfig.NewSecretScanner()
		if err != nil {
			return nil, err
		}
		if file, err = initconfig.Load(path, scanner); err != nil {
			return nil, err
		}
	}
	return initconfig.Resolve(initconfig.Flags{
		ConfigPath: path, Yes: f.yes, Profile: f.profile,
		Harnesses: f.harnesses, NoDaemon: f.noDaemon,
	}, initconfig.OSEnv, file)
}

// initOptions maps the flags onto the wizard's options.
//
// --check wins over --yes when both are given: the two disagree about
// whether to write, and the safe reading of a contradictory instruction
// is the one that changes nothing.
func initOptions(f *initFlags) cascadeinit.Options {
	mode := cascadeinit.ModeInteractive
	switch {
	case f.check:
		mode = cascadeinit.ModeCheck
	case f.yes:
		mode = cascadeinit.ModeYes
	}
	return cascadeinit.Options{
		Mode: mode, Profile: f.profile, Harnesses: f.harnesses,
		NoDaemon: f.noDaemon, Reconverge: f.reconverge,
	}
}

// initPrompter picks the prompter for the mode: a real terminal reader
// for an interactive run, and the answer-for-the-operator one otherwise.
func initPrompter(cmd *cobra.Command, f *initFlags) cascadeinit.Prompter {
	if f.yes || f.check {
		return cascadeinit.DefaultPrompter{}
	}
	return cascadeinit.NewTTYPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
}

// productionInitDeps wires the wizard to the real implementations.
func productionInitDeps(cmd *cobra.Command, paths runtime.PathProvider) (cascadeinit.Deps, error) {
	cwd, err := initCwd()
	if err != nil {
		return cascadeinit.Deps{}, err
	}
	f := &initFlags{}
	if flag := cmd.Flags().Lookup("yes"); flag != nil {
		f.yes = flag.Value.String() == "true"
	}
	if flag := cmd.Flags().Lookup("check"); flag != nil {
		f.check = flag.Value.String() == "true"
	}
	return cascadeinit.Deps{
		Home: paths.Root(),
		// The SAME expression every other composition-root site uses to
		// reach this installation's database (daemon_unix_store.go,
		// recall_embedded.go, doctor_recall_index.go and the rest). The
		// wizard is handed it rather than deriving its own, so `init`
		// cannot advertise a path nothing opens (R-14.279).
		LocalDBPath: filepath.Join(paths.DataDir(), "cascade.db"),
		Cwd:         cwd,
		GOOS:        goruntime.GOOS,
		Out:         cmd.OutOrStdout(),
		Prompt:      initPrompter(cmd, f),
		Detector:    initHarnessDetector(),
		Wirer:       initHarnessWirer{},
		Catalog:     initPluginCatalog{paths: paths, clock: runtime.NewSystemClock()},
		Service:     initServiceInstaller{paths: paths},
		Enroller:    initHelperEnroller{},
		Doctor:      initDoctorRunner{paths: paths},
		Sub: initSubprocess{
			in: cmd.InOrStdin(), out: cmd.OutOrStdout(), err: cmd.ErrOrStderr(),
		},
		Storage: initStorageProbe{},
		Secrets: initSecretGuard{},
		Getenv:  initGetenv,
	}, nil
}

// initHarnessDetector returns the REAL S-35.T3 detector. The wizard
// consumes it; there is no second path-probing implementation anywhere in
// the init package.
func initHarnessDetector() cascadeinit.Detector {
	return harnessDetectorAdapter{
		inner: cascadecontext.NewPathDetector(goruntime.GOOS, initGetenv, initPathExists).
			WithFileReader(os.ReadFile),
	}
}

// harnessDetectorAdapter narrows internal/context's HarnessState to the
// three fields the wizard renders.
type harnessDetectorAdapter struct {
	inner cascadecontext.HarnessDetector
}

// Detect implements the wizard's Detector over the real one.
func (a harnessDetectorAdapter) Detect(ctx context.Context) ([]cascadeinit.HarnessState, error) {
	states, err := a.inner.Detect(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cascadeinit.HarnessState, 0, len(states))
	for _, s := range states {
		out = append(out, cascadeinit.HarnessState{
			Kind: string(s.Kind), Detected: s.Detected, InstallPath: s.InstallPath,
		})
	}
	return out, nil
}
