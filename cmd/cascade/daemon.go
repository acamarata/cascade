// Purpose: the `cascade daemon` subcommand group (07-CLI-COMMAND-TREE.md
//
//	§daemon) — run/start/stop/restart/status/install/uninstall — mounted on
//	the root cobra tree D/S-06.T1 built. This file is the platform-
//	independent half: cobra wiring, config loading, and internal/output
//	rendering. The lifecycle verbs' actual work is platform-specific
//	(daemon_unix.go / daemon_windows.go, R-14.117 sibling split — Windows
//	refuses every verb with the same typed error, unix does the real work)
//	so this file never branches on GOOS itself for those; it calls
//	platformDaemon{Run,Start,Stop,Restart,Status}, whose SYMBOL differs by
//	which file the build tag selected, matching this repo's established
//	pattern (hotreload_signal_windows.go). install/uninstall (D/S-07.T2)
//	follow the SAME never-branch-on-GOOS discipline but without a sibling
//	daemon_unix.go/daemon_windows.go split of their own: the platform
//	difference lives entirely inside internal/daemon/service.NewInstaller()
//	(itself build-tag-selected), so this file only ever holds a
//	service.Installer interface value.
//
// Inputs: cobra args/flags; a daemonDeps injected at construction so no
//
//	test touches the real home directory or process environment (Art.7.1).
//
// Outputs: process output via internal/output.Writer; a taxonomy error on
//
//	failure. NEVER prints via os.Stdout/os.Stderr directly (forbidigo +
//	internal/build's AST output gate enforce this across cmd/**).
//
// Constraints: Art.10.2 — cmd/ is the sole composition root; internal/
//
//	daemon and internal/daemon/service take every dependency by injection
//	and never reach for the real environment themselves.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates; CHANGE, per
//
//	D/S-07.T2 sport_updates — adds install/uninstall).
package main

import (
	"context"
	"os"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// daemonDeps carries every external input the daemon command tree needs.
type daemonDeps struct {
	Paths      runtime.PathProvider
	Getenv     runtime.Getenv
	Environ    func() []string
	Clock      runtime.Clock
	Executable func() (string, error)
	// HomeDir resolves the real user home directory install/uninstall's
	// service units are rooted under (~/Library/LaunchAgents, ~/.config/
	// systemd/user) — distinct from Paths.Root() (CASCADE_HOME). Injected
	// so tests never touch the real home directory (Art.7.1).
	HomeDir func() (string, error)
	// Getuid resolves the effective uid launchd's gui/<uid> domain target
	// needs on darwin. Injected (rather than called directly from a
	// command's RunE) purely so this file never reaches for the real
	// environment itself, matching every other daemonDeps field.
	Getuid func() int
	// Installer is the platform service-unit manager (darwin launchd,
	// linux systemd, windows typed refusal) install/uninstall drive.
	// Resolved once via service.NewInstaller() in production; every test
	// injects a fake.
	Installer service.Installer
	// NodeTunnels is the controller-side ssh tunnel registry (S-36.T3):
	// `node enroll`/`node status` (S-36.T4) start/read tunnels through it.
	// A nil value (every existing test's zero-value daemonDeps) is valid —
	// see startNodeTunnelService's own nil guard.
	NodeTunnels *nodes.Manager
}

// productionDaemonDeps builds daemonDeps against the real environment. The
// path resolution is deferred exactly like root.go's lazyPaths: constructing
// the command tree must never touch the environment, only running a command
// does.
func productionDaemonDeps() daemonDeps {
	return daemonDeps{
		Paths:       lazyPaths{},
		Getenv:      os.Getenv,
		Environ:     os.Environ,
		Clock:       runtime.SystemClock{},
		Executable:  os.Executable,
		HomeDir:     os.UserHomeDir,
		Getuid:      os.Getuid,
		Installer:   service.NewInstaller(),
		NodeTunnels: nodes.NewManager(),
	}
}

// newDaemonCmd builds the `daemon` command tree.
func newDaemonCmd(deps daemonDeps) *cobra.Command {
	root := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the cascade daemon process",
	}
	root.AddCommand(newDaemonRunCmd(deps))
	root.AddCommand(newDaemonStartCmd(deps))
	root.AddCommand(newDaemonStopCmd(deps))
	root.AddCommand(newDaemonRestartCmd(deps))
	root.AddCommand(newDaemonStatusCmd(deps))
	root.AddCommand(newDaemonInstallCmd(deps))
	root.AddCommand(newDaemonUninstallCmd(deps))
	root.AddCommand(newDaemonLogsCmd(deps))
	return root
}

func newDaemonRunCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			startNodeTunnelService(deps)
			return platformDaemonRun(cmd.Context(), deps)
		},
	}
}

// startNodeTunnelService registers the controller-side ssh tunnel service
// (S-36.T3) with deps.NodeTunnels. WINDOWS (R-21.226):
// nodes.RefuseTunnelServiceOnGOOS's refusal is honored by returning
// without starting anything — the daemon still runs, only the tunnel
// service is unsupported there (mirrors node_serve.go's per-verb refusal
// rather than failing `daemon run` outright). EVERY OTHER PLATFORM —
// CONTRADICTION (full quote in the ticket journal): per-node auto-start
// is not implemented here because Target has no durable source yet —
// DeviceRecord (records.go, S-36.T1, out of this ticket's files_scope)
// carries no Host/User field. deps.NodeTunnels is still real, wired
// infrastructure: S-36.T4's `node enroll`/`node status` verbs call
// Manager.Start/State on it directly once they land.
func startNodeTunnelService(deps daemonDeps) {
	if deps.NodeTunnels == nil {
		return
	}
	if err := nodes.RefuseTunnelServiceOnGOOS(goruntime.GOOS); err != nil {
		return
	}
	// No enrolled-node Target source exists yet (see doc comment above);
	// deps.NodeTunnels is registered and ready for S-36.T4 to drive.
}

func newDaemonStartCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := platformDaemonStart(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(res)
		},
	}
}

func newDaemonStopCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := platformDaemonStop(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(res)
		},
	}
}

func newDaemonRestartCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon (stop, then start)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := platformDaemonRestart(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(res)
		},
	}
}

func newDaemonStatusCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := platformDaemonStatus(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(res)
		},
	}
}

// outputWriter builds an internal/output.Writer bound to cmd's own streams
// and the standard --json/-q/-v/--no-color flags, matching cmd/cascade/
// config's outputWriter (duplicated: that helper is unexported in package
// config and this file lives in package main).
func outputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// loadDaemonConfig loads config.toml through runtime.Load, as every CLI
// command does (single resolution model, 08 §2), and resolves the [daemon]
// Settings (socket, shutdown_grace) from it, never hardcoded.
func loadDaemonConfig(ctx context.Context, deps daemonDeps) (*runtime.Config, runtime.PathProvider, daemon.Settings, error) {
	paths := deps.Paths
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		// A Kind already on the error (a refused config file is
		// permission_denied) is kept; only an untyped one is classified.
		if _, typed := cascade.KindOf(err); !typed {
			err = cascade.Wrap(cascade.KindInvalidInput, err, "load config.toml")
		}
		return nil, nil, daemon.Settings{}, err
	}
	settings, err := daemon.ResolveSettings(cfg, paths)
	if err != nil {
		return nil, nil, daemon.Settings{}, err
	}
	return cfg, paths, settings, nil
}

// productionElevationPrecondition wires the real §D-24 daemonless
// elevation precondition check (internal/policy.IsDaemonlessElevationAllowed's
// two bool inputs) against custody and the trust store. A resolution failure (paths
// unavailable) fails closed: both preconditions report false, matching
// R-14.163's "cannot prove available" default the same way
// internal/runtime.DaemonlessElevationPrecondition already does for a nil
// function.
func productionElevationPrecondition(paths runtime.PathProvider) runtime.ElevationPrecondition {
	return custodyElevationPrecondition(paths, elevation.SelectCustody)
}

func custodyElevationPrecondition(paths runtime.PathProvider, selectCustody func(string) elevation.Custody) runtime.ElevationPrecondition {
	return func() (helperEnrolled, authenticatorAvailable bool) {
		if paths == nil || paths.DataDir() == "" {
			return false, false
		}
		custody := selectCustody(paths.DataDir())
		trust := elevation.NewElevationTrustStore(elevation.NewFileBackend(paths.DataDir()), runtime.SystemClock{})
		return trust.IsEnrolled(), custody.Tier().SatisfiesElevation()
	}
}

// relaunchArgs builds the "daemon run" argument list Start's background
// spawn re-invokes the binary with, carrying forward the same --config/
// --profile the parent CLI process saw so the relaunched daemon resolves
// the identical config.toml (globalFlags is root.go's own package-level
// state, populated by cobra during the parent process's Execute()).
func relaunchArgs() []string {
	args := []string{"daemon", "run"}
	if globalFlags.Config != "" {
		args = append(args, "--config", globalFlags.Config)
	}
	if globalFlags.Profile != "" {
		args = append(args, "--profile", globalFlags.Profile)
	}
	return args
}

// statusView is StatusResult flattened into the shape `cascade daemon
// status --json`'s versioned envelope carries (pid/uptime_s/connections,
// per this ticket's acceptance criteria) plus a human Detail line; both
// platform files convert their own daemon.StatusResult into this common
// type so this file never imports a platform-specific result shape.
type statusView struct {
	Running     bool    `json:"running"`
	PID         int     `json:"pid"`
	UptimeS     float64 `json:"uptime_s"`
	Connections int     `json:"connections"`
	Detail      string  `json:"detail"`
}
