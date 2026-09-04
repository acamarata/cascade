// Purpose: cobra root command definition and persistent global flags.
// Inputs:  process args, parsed by cobra into GlobalFlags.
// Outputs: the constructed root *cobra.Command tree (root + version + completion
//
//	at this ticket; later tickets mount further command groups per
//	07-CLI-COMMAND-TREE.md against this same root).
//
// Constraints: pure CLI wiring only — no business logic (06-FORGE-SPEC §2).
//
//	Global flags are declared exactly per 07-CLI-COMMAND-TREE.md §global-flags:
//	--json --profile --config -q/--quiet -v/--verbose.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/cmd/cascade/config"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// daemonlessProbeTimeout bounds the socket-probe dial PersistentPreRunE
// runs before every command. Matches probeSocket's own 2s production
// default (internal/runtime/recovery_scan.go).
const daemonlessProbeTimeout = 2 * time.Second

// GlobalFlags holds the persistent flag values shared by every subcommand.
// Subcommands read this struct directly (same package) or, from outside the
// package, via cmd.Root().PersistentFlags().
type GlobalFlags struct {
	// JSON requests the versioned JSON envelope output contract (D/S-06.T5).
	JSON bool
	// Profile selects a named config profile.
	Profile string
	// Config overrides the config file path.
	Config string
	// Quiet suppresses progress output.
	Quiet bool
	// Verbose increases log verbosity.
	Verbose bool
}

var globalFlags GlobalFlags

// newRootCmd constructs the cascade root command with its persistent global
// flags and mounts the subcommands owned by this ticket. Later tickets add
// further subcommands (e.g. D/S-06.T2's `daemon` group) against this same
// root without needing to change this function's shape.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cascade",
		Short:         "Cascade: a local-first AI agent runtime",
		Long:          "Cascade is a local-first AI agent runtime: one binary that is both\nthe CLI surface and, via \"cascade daemon run\", the long-lived daemon.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Root needs a RunE: without one cobra prints help and never invokes
		// PersistentPreRunE, so global-flag validation would silently not run.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		// Cobra's default validator for a command with subcommands reports an
		// unknown subcommand as a bare error, which reaches main kindless and
		// exits internal(1) — wrong for what is the commonest CLI mistake there
		// is, a typo'd subcommand. Reproduce cobra's own message and give it the
		// invalid-input kind (R-14.113).
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return cascade.Newf(cascade.KindInvalidInput,
					"unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if globalFlags.Quiet && globalFlags.Verbose {
				return cascade.New(cascade.KindInvalidInput, "--quiet and --verbose are mutually exclusive")
			}
			cmd.SetContext(probeDaemonlessAndAttach(cmd.Context()))
			return nil
		},
	}

	// Cobra's flag parser returns bare errors; without this they would reach
	// main as kindless errors and exit 1 (internal) instead of the taxonomy's
	// invalid-input status. R-14.113.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return cascade.Wrap(cascade.KindInvalidInput, err, "invalid flag")
	})

	flags := root.PersistentFlags()
	flags.BoolVar(&globalFlags.JSON, "json", false, "emit output as a versioned JSON envelope")
	flags.StringVar(&globalFlags.Profile, "profile", "", "select a named config profile")
	flags.StringVar(&globalFlags.Config, "config", "", "override the config file path")
	flags.BoolVarP(&globalFlags.Quiet, "quiet", "q", false, "suppress progress output")
	flags.BoolVarP(&globalFlags.Verbose, "verbose", "v", false, "increase log verbosity")

	// Registered here, not in main(): the binary and newRootCmd() must build
	// the SAME tree, or the golden-help fixture can only ever match one of
	// them and every later command lands on a tree the tests do not see.
	registerNoColorFlag(root)

	mountSubcommands(root)

	return root
}

// mountSubcommands attaches every command group to root. It is a separate
// function only so newRootCmd stays inside the 50-line cap as the tree
// grows; the list itself is the composition, and it stays in one place so
// there is exactly one tree the binary, the tests and the golden help
// fixture all see.
func mountSubcommands(root *cobra.Command) {
	root.AddCommand(newVersionCmd())
	root.AddCommand(newCompletionCmd(root))
	mountConfigCmd(root)
	mountDaemonCmd(root)
	mountMCPCmd(root)
	mountStatusCmd(root)
	mountDoctorCmd(root)
	mountElevateHelperCmd(root)
	mountVaultCmd(root)
	mountMemoryCmd(root)
	mountRecallCmd(root)
}

// mountMCPCmd attaches the `mcp` command tree (D/S-06.T6), following
// mountDaemonCmd's exact pattern.
func mountMCPCmd(root *cobra.Command) {
	cmd := newMCPCmd(productionMCPDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// mountDaemonCmd attaches the `daemon` command tree (D/S-06.T2), following
// mountConfigCmd's exact pattern: cmd/cascade/daemon.go's newDaemonCmd is
// package-local (daemon.go lives in package main, unlike config's
// subpackage), so this is a direct call rather than an import, but the
// deferred-environment-resolution and guardUnknownSubcommands treatment are
// identical.
func mountDaemonCmd(root *cobra.Command) {
	cmd := newDaemonCmd(productionDaemonDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// usageArgs adapts a cobra positional-argument validator so its errors carry
// the invalid-input taxonomy kind. Cobra builds these errors internally and
// they would otherwise reach main kindless and exit internal(1) instead of
// invalid-input(2). Every command with an Args validator wraps it (R-14.113).
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return cascade.Wrap(cascade.KindInvalidInput, err, "invalid arguments")
		}
		return nil
	}
}

// mountConfigCmd attaches the `config` command tree. It lives here because
// cmd/ is the sole composition root (Art.10.2): the config package takes its
// dependencies by injection and must not reach for the real environment
// itself.
//
// The command tree MUST NOT depend on the environment. Resolving the path
// provider here and skipping the mount on failure would make `cascade --help`
// print a different tree on a machine where the home directory cannot be
// resolved -- an environment-dependent CLI surface, and a golden-help test
// that flakes by machine (Art.11). So the subcommand is always mounted, and a
// resolution failure surfaces when a config command is actually RUN, as a
// taxonomy error with the right exit code.
func mountConfigCmd(root *cobra.Command) {
	cmd := config.NewConfigCmd(config.Deps{
		Paths:   lazyPaths{},
		Getenv:  os.Getenv,
		Clock:   runtime.SystemClock{},
		Environ: os.Environ,
	})
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// guardUnknownSubcommands applies the root's unknown-command rule to a whole
// mounted subtree. Without it, `cascade config bogus` prints help and exits
// 0: cobra's default for a group command with no Run is to show help, and the
// invalid-input mapping applied to the root does not reach commands mounted
// under it. Every command group added later would inherit that, so the guard
// belongs at the mount point rather than in each group's own package.
func guardUnknownSubcommands(cmd *cobra.Command) {
	switch {
	case cmd.Args != nil:
		// Wrap the command's own validator. Cobra builds these errors
		// itself, so they carry no taxonomy kind and would exit
		// internal(1) rather than invalid-input(2) -- which is why
		// `cascade config get` with no argument exited 1.
		cmd.Args = usageArgs(cmd.Args)
	case len(cmd.Commands()) > 0:
		// A group command with no validator. Setting Args alone is NOT
		// enough: cobra returns ErrHelp before validating args when a
		// command is not Runnable, so the validator never runs and an
		// unknown subcommand prints help and exits 0. Give the group a
		// RunE that shows help -- exactly the fix the root command needed
		// for the same reason -- so validation is reached.
		cmd.Args = func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				return cascade.Newf(cascade.KindInvalidInput,
					"unknown command %q for %q", args[0], c.CommandPath())
			}
			return nil
		}
		if cmd.Run == nil && cmd.RunE == nil {
			cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
		}
	}
	for _, sub := range cmd.Commands() {
		guardUnknownSubcommands(sub)
	}
}

// lazyPaths defers NewDefaultPathProvider to first use, so constructing the
// command tree never touches the environment. Each accessor resolves on
// demand and returns the zero value if resolution fails; the config commands
// validate the paths they receive and report the failure themselves.
type lazyPaths struct{}

func (lazyPaths) resolve() runtime.PathProvider {
	p, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return nil
	}
	return p
}

func (l lazyPaths) get(f func(runtime.PathProvider) string) string {
	p := l.resolve()
	if p == nil {
		return ""
	}
	return f(p)
}

func (l lazyPaths) Root() string {
	return l.get(func(p runtime.PathProvider) string { return p.Root() })
}
func (l lazyPaths) ConfigPath() string {
	return l.get(func(p runtime.PathProvider) string { return p.ConfigPath() })
}
func (l lazyPaths) SocketPath() string {
	return l.get(func(p runtime.PathProvider) string { return p.SocketPath() })
}
func (l lazyPaths) DataDir() string {
	return l.get(func(p runtime.PathProvider) string { return p.DataDir() })
}
func (l lazyPaths) LogDir() string {
	return l.get(func(p runtime.PathProvider) string { return p.LogDir() })
}
func (l lazyPaths) StorageRoot(profile runtime.Profile) string {
	return l.get(func(p runtime.PathProvider) string { return p.StorageRoot(profile) })
}

// probeDaemonlessAndAttach is the §D-3 socket-probe auto-fallback: run
// ALWAYS, for every command, before any subcommand's RunE — there is no
// --daemonless flag (07-CLI-COMMAND-TREE's global-flag set is fixed),
// this probe is the only activation path. It stats+dials the socket via
// runtime.ProbeDaemonless (which itself reuses probeSocket, the SAME
// function recovery scanning uses — no second probe), attaches the
// resulting DaemonlessState to ctx for every command/subsystem to read
// via runtime.DaemonlessStateFrom, and — in non-quiet mode — emits a
// stderr notice through internal/output (never a bare fmt.Print) when
// embedded mode activates.
//
// A path-resolution failure (e.g. HOME unresolvable) is reported the same
// way lazyPaths handles it elsewhere in this file: the probe is skipped,
// ctx is returned unchanged, and DaemonlessStateFrom's ok=false tells a
// caller "unknown," never "confirmed not embedded" — never a guessed
// answer standing in for a real one.
func probeDaemonlessAndAttach(ctx context.Context) context.Context {
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return ctx
	}
	st := runtime.ProbeDaemonless(paths.SocketPath(), daemonlessProbeTimeout, nil)
	if st.Embedded && !globalFlags.Quiet {
		w := output.NewDefault(globalFlags.JSON, globalFlags.Quiet, globalFlags.Verbose, noColorFlag)
		if st.ProbeErr != nil {
			w.Warn("daemon liveness undecidable (%v); running in embedded (daemonless) mode", st.ProbeErr)
		} else {
			w.Warn("daemon not running; running in embedded (daemonless) mode")
		}
	}
	return runtime.WithDaemonlessState(ctx, st)
}
