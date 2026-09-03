// Purpose: the `cascade daemon` subcommand group (07-CLI-COMMAND-TREE.md
//
//	§daemon) — run/start/stop/restart/status — mounted on the root cobra
//	tree D/S-06.T1 built. This file is the platform-independent half: cobra
//	wiring, config loading, and internal/output rendering. The actual
//	lifecycle calls are platform-specific (daemon_unix.go / daemon_windows.
//	go, R-14.117 sibling split — Windows refuses every verb with the same
//	typed error, unix does the real work) so this file never branches on
//	GOOS itself; it calls platformDaemon{Run,Start,Stop,Restart,Status},
//	whose SYMBOL differs by which file the build tag selected, matching
//	this repo's established pattern (hotreload_signal_windows.go).
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
//	daemon takes every dependency by injection and never reaches for the
//	real environment itself.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates).
package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon"
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
}

// productionDaemonDeps builds daemonDeps against the real environment. The
// path resolution is deferred exactly like root.go's lazyPaths: constructing
// the command tree must never touch the environment, only running a command
// does.
func productionDaemonDeps() daemonDeps {
	return daemonDeps{
		Paths:      lazyPaths{},
		Getenv:     os.Getenv,
		Environ:    os.Environ,
		Clock:      runtime.SystemClock{},
		Executable: os.Executable,
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
	return root
}

func newDaemonRunCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return platformDaemonRun(cmd.Context(), deps)
		},
	}
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
// config's outputWriter exactly (both read the same persistent root flags;
// duplicated rather than shared because that helper is unexported in
// package config and this file lives in package main).
func outputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// loadDaemonConfig loads config.toml through the same runtime.Load entry
// point every other CLI command uses (single resolution model, 08 §2) and
// resolves this ticket's [daemon] Settings against it — socket and
// shutdown_grace sourced from the config loader, never hardcoded, per this
// ticket's contract.
func loadDaemonConfig(ctx context.Context, deps daemonDeps) (*runtime.Config, runtime.PathProvider, daemon.Settings, error) {
	paths := deps.Paths
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return nil, nil, daemon.Settings{}, cascade.Wrap(cascade.KindInvalidInput, err, "load config.toml")
	}
	settings, err := daemon.ResolveSettings(cfg, paths)
	if err != nil {
		return nil, nil, daemon.Settings{}, err
	}
	return cfg, paths, settings, nil
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
