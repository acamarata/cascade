// Purpose: `cascade widget install|uninstall|status [--json]` - the macOS
//
//	menu-bar widget's LaunchAgent, a thin command layer over
//	internal/daemon/service's WidgetInstaller. Non-interactive and idempotent:
//	a repeat install reports "reloaded", an uninstall of an absent unit
//	reports "not installed", both exit 0. On linux and windows every verb
//	refuses with the typed unsupported error naming the verb.
//
// Inputs: widgetDeps, injected so no test touches the real HOME, launchctl,
//
//	clock or executable path.
//
// Outputs: process output via internal/output.Writer (DeltaReport-shaped
//
//	install/uninstall, StatusReport-shaped status); a taxonomy error on
//	failure.
//
// Constraints: no bare os.Getenv/os.Executable here; production wiring is
//
//	productionWidgetDeps only. The bundle path is never read from the
//	environment (see the service package).
//
// SPORT: cmd/cascade/widget (P1-WID-04).
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// widgetDeps carries every external input the widget commands need.
type widgetDeps struct {
	Installer  service.WidgetInstaller
	HomeDir    func() (string, error)
	Getuid     func() int
	Executable func() (string, error)
	Paths      runtime.PathProvider
	Getenv     runtime.Getenv
	Clock      runtime.Clock
	Runner     service.Runner
	Output     service.OutputRunner
}

// productionWidgetDeps builds widgetDeps against the real environment.
func productionWidgetDeps() widgetDeps {
	return widgetDeps{
		Installer:  service.NewWidgetInstaller(),
		HomeDir:    os.UserHomeDir,
		Getuid:     os.Getuid,
		Executable: os.Executable,
		Paths:      lazyPaths{},
		Getenv:     os.Getenv,
		Clock:      runtime.SystemClock{},
		Runner:     service.ExecRunner{},
		Output:     service.ExecOutputRunner{},
	}
}

// mountWidgetCmd attaches the top-level `widget` command.
func mountWidgetCmd(root *cobra.Command) {
	cmd := newWidgetCmd(productionWidgetDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newWidgetCmd builds `widget` and its three subcommands.
func newWidgetCmd(deps widgetDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "widget",
		Short: "Install and inspect the macOS menu-bar widget",
		Long: "Manage the LaunchAgent that starts the cascade menu-bar widget at login.\n" +
			"macOS only; other platforms refuse with an unsupported error.",
	}
	cmd.AddCommand(newWidgetInstallCmd(deps), newWidgetUninstallCmd(deps), newWidgetStatusCmd(deps))
	return cmd
}

func newWidgetInstallCmd(deps widgetDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the widget LaunchAgent (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := widgetServiceConfig(deps)
			if err != nil {
				return err
			}
			report, err := deps.Installer.Install(cfg)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(widgetInstallView{report})
		},
	}
}

func newWidgetUninstallCmd(deps widgetDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the widget LaunchAgent (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := widgetServiceConfig(deps)
			if err != nil {
				return err
			}
			report, err := deps.Installer.Uninstall(cfg)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(serviceDeltaView{report})
		},
	}
}

func newWidgetStatusCmd(deps widgetDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the widget is installed, loaded and where it runs from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := widgetServiceConfig(deps)
			if err != nil {
				return err
			}
			report, err := deps.Installer.Status(cfg)
			if err != nil {
				return err
			}
			return outputWriter(cmd).Result(widgetStatusView{report})
		},
	}
}

// widgetServiceConfig assembles the service.WidgetConfig from widgetDeps.
func widgetServiceConfig(deps widgetDeps) (service.WidgetConfig, error) {
	if r, ok := deps.Paths.(interface{ ResolveErr() error }); ok {
		if err := r.ResolveErr(); err != nil {
			return service.WidgetConfig{}, cascade.Wrap(cascade.KindUnavailable, err, "resolve cascade paths")
		}
	}
	home, err := deps.HomeDir()
	if err != nil {
		return service.WidgetConfig{}, cascade.Wrap(cascade.KindUnavailable, err, "resolve home directory")
	}
	execPath, err := deps.Executable()
	if err != nil {
		return service.WidgetConfig{}, cascade.Wrap(cascade.KindUnavailable, err, "resolve cascade executable path")
	}
	return service.WidgetConfig{
		HomeDir: home, UID: deps.Getuid(), Executable: execPath, Paths: deps.Paths,
		Getenv: deps.Getenv, Clock: deps.Clock, Runner: deps.Runner, Output: deps.Output,
	}, nil
}

// widgetInstallView renders an install report; JSON carries installed_at.
type widgetInstallView struct{ service.WidgetInstallReport }

// String leads with the action, then the install time.
func (v widgetInstallView) String() string {
	return fmt.Sprintf("%s: %s (installed_at %s)", v.Action, v.Detail, v.InstalledAt)
}

// widgetStatusView renders a status report.
type widgetStatusView struct{ service.WidgetStatusReport }

// String lists the state line by line, naming both bundle paths on a
// placement problem.
func (v widgetStatusView) String() string {
	if !v.Installed {
		return "not installed (run `cascade widget install`)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "installed: %s\n", v.PlistPath)
	if !v.Managed {
		b.WriteString("managed: no (not written by cascade; inspect and remove it)")
		return b.String()
	}
	if !v.ProgramValid {
		b.WriteString("managed: yes\nprogram valid: no (it differs from what cascade installs; run `cascade widget uninstall` then `cascade widget install`, or inspect it)")
		return b.String()
	}
	loaded := "no"
	if v.Loaded {
		loaded = "yes"
		if v.State != "" {
			loaded += " (" + v.State + ")"
		}
	}
	fmt.Fprintf(&b, "loaded: %s\nbundle: %s", loaded, v.BundlePath)
	if !v.BundlePresent {
		b.WriteString(" (missing)")
	}
	if v.SelfReportError != "" {
		fmt.Fprintf(&b, "\nself-report unreadable: %s", v.SelfReportError)
	}
	if r := v.SelfReport; r != nil {
		fmt.Fprintf(&b, "\nreported bundle: %s", r.BundlePath)
		if r.WindowLevel != nil {
			fmt.Fprintf(&b, "\nwindow level: %d", *r.WindowLevel)
		}
	}
	if v.Placement != service.PlacementOK {
		fmt.Fprintf(&b, "\nplacement: %s (installed %s, running %s)", v.Placement, v.BundlePath, v.SelfReport.BundlePath)
	}
	return b.String()
}
