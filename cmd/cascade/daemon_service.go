package main

// Purpose: `cascade daemon install`/`uninstall` (D/S-07.T2,
//   07-CLI-COMMAND-TREE.md §daemon) — split out of daemon.go purely to
//   satisfy Art.10.3's 300-line file cap once these two subcommands and
//   their shared service.Config assembly were added (R-14.117: a ticket
//   may split a file it owns into additional sibling files in the same
//   package to satisfy the cap; the split is behaviour-preserving, moved
//   code only). daemon.go's newDaemonCmd still mounts both commands this
//   file builds; see daemon.go's package doc for the platform-neutrality
//   discipline both files share.
// Inputs: the same daemonDeps every other daemon subcommand takes.
// Outputs: process output via internal/output.Writer (install/uninstall's
//   DeltaReport); a taxonomy error on failure.
// Constraints: Art.10.2 — every environment touchpoint (HomeDir, Getuid,
//   Executable, the Installer itself) comes from daemonDeps, never a bare
//   call here.
// SPORT: cmd/cascade/daemon (CHANGE, per D/S-07.T2 sport_updates).

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newDaemonInstallCmd wires `cascade daemon install`: generates and
// registers the platform-native service unit (launchd user agent on
// darwin, systemd user unit on linux; a typed refusal on windows). Fully
// non-interactive (§5.8) and idempotent (§5.9) — a second run converges
// and still exits 0.
func newDaemonInstallCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the cascade daemon as a platform service (launchd/systemd)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := daemonServiceConfig(cmd.Context(), deps)
			if err != nil {
				return err
			}
			report, err := deps.Installer.Install(cfg)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(report)
		},
	}
}

// newDaemonUninstallCmd wires `cascade daemon uninstall`: the exact
// inverse of install. Idempotent — uninstalling an absent unit is a clean
// no-op, never a non-zero exit (§5.9).
func newDaemonUninstallCmd(deps daemonDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the cascade daemon's platform service unit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := daemonServiceConfig(cmd.Context(), deps)
			if err != nil {
				return err
			}
			report, err := deps.Installer.Uninstall(cfg)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(report)
		},
	}
}

// daemonServiceConfig assembles a service.Config from daemonDeps: the
// real home directory and uid come from the injected HomeDir/Getuid (never
// a bare os.UserHomeDir()/os.Getuid() call here — Art.10.2), while the
// executable path and log path reuse the exact same config-loader and
// os.Executable() resolution the start/restart verbs already use, so
// install's generated unit points at the same binary and log file a
// `cascade daemon start` would.
func daemonServiceConfig(ctx context.Context, deps daemonDeps) (service.Config, error) {
	_, paths, _, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return service.Config{}, err
	}
	execPath, err := deps.Executable()
	if err != nil {
		return service.Config{}, cascade.Wrap(cascade.KindUnavailable, err, "resolve cascade executable path")
	}
	home, err := deps.HomeDir()
	if err != nil {
		return service.Config{}, cascade.Wrap(cascade.KindUnavailable, err, "resolve home directory")
	}
	return service.Config{
		HomeDir:    home,
		Executable: execPath,
		LogPath:    runtime.LogFilePath(paths),
		UID:        deps.Getuid(),
		Runner:     service.ExecRunner{},
	}, nil
}
