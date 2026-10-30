package config

// Purpose: `cascade config reload` — reads the daemon's pidfile and sends
//   it SIGHUP (08 §3: "cascade config reload forces re-read"); no running
//   daemon is not an error (exit 0, informational message). The actual
//   signal-send is platform-split (config_reload_unix.go /
//   config_reload_windows.go): SIGHUP has no Windows equivalent, so the
//   Windows build returns an explicit tier-2 refusal rather than
//   silently doing nothing (Art.5 platform parity — this ticket's
//   contract calls this out by name for the daemon-side hot-reload path;
//   the CLI's own reload-send follows the same rule for consistency).
// Inputs: deps.Paths.Root()/"daemon.pid" (no daemon lifecycle/pidfile
//   writer exists anywhere in this tree yet — D/S-06.T2's territory —
//   so this path is a documented convention this ticket establishes,
//   consistent with PathProvider's existing SocketPath()/ConfigPath()
//   naming; the "no pidfile" branch is fully real and tested today, the
//   "pidfile present" branch is real code with no daemon yet alive to
//   exercise it end-to-end — see this ticket's final report).
// Outputs: informational text via internal/output.Writer.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// pidFilePath is this ticket's documented convention for where a running
// daemon's PID is recorded. No daemon lifecycle ticket has landed yet to
// author a different one; the moment D/S-06.T2 does, `sendReloadSignal`
// and this constant are the only things that need to change.
func pidFilePath(root string) string {
	return filepath.Join(root, "daemon.pid")
}

// readDaemonPID reads and parses the pidfile, reporting found=false (not
// an error) when it does not exist.
func readDaemonPID(root string) (pid int, found bool, err error) {
	data, err := os.ReadFile(pidFilePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false, &cascade.Error{Kind: cascade.KindIntegrity, Msg: "daemon.pid is not a valid PID"}
	}
	return pid, true, nil
}

func newReloadCmd(deps Deps) *cobra.Command {
	var acceptLoosening bool
	cmd := &cobra.Command{
		Use:   "reload",
		Short: "Send the running daemon SIGHUP to reload config.toml",
		Long: "Send the running daemon SIGHUP to reload config.toml. A reload never applies a\n" +
			"loosening of a guarded value; --accept-loosening submits the elevated\n" +
			"config.accept_loosening for the file's exact bytes, which reloads it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := outputWriter(cmd)
			if acceptLoosening {
				return reloadAcceptingLoosening(cmd, deps)
			}
			pid, found, err := readDaemonPID(deps.Paths.Root())
			if err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "read daemon pidfile")
			}
			if !found {
				return w.Result("no running daemon (no pidfile found); nothing to reload")
			}
			if err := sendReloadSignalToPID(pid); err != nil {
				return err
			}
			return w.Result("sent reload signal to daemon")
		},
	}
	cmd.Flags().BoolVar(&acceptLoosening, "accept-loosening", false,
		"accept the on-disk config.toml's loosening through an elevated config.accept_loosening")
	return cmd
}

// reloadAcceptingLoosening submits config.accept_loosening for the sha256 of
// the config file as it is on disk now. No daemon client is unavailable.
func reloadAcceptingLoosening(cmd *cobra.Command, deps Deps) error {
	if deps.AcceptLoosening == nil {
		return cascade.New(cascade.KindUnavailable, "config reload --accept-loosening: no daemon client")
	}
	data, err := readConfigBytes(deps.Paths.ConfigPath())
	if err != nil {
		return err
	}
	if data == nil {
		return cascade.New(cascade.KindInvalidInput, "config reload --accept-loosening: there is no config.toml to accept")
	}
	if err := deps.AcceptLoosening(cmd.Context(), runtime.ConfigDigest(data)); err != nil {
		return err
	}
	return outputWriter(cmd).Result("the daemon accepted config.toml's loosening and reloaded it")
}

// sendReloadSignal is triggerReload's best-effort helper (config_write.go):
// reads the pidfile and sends the platform reload signal, swallowing a
// "no daemon running" condition (not an error there either).
func sendReloadSignal(deps Deps) error {
	pid, found, err := readDaemonPID(deps.Paths.Root())
	if err != nil || !found {
		return err
	}
	return sendReloadSignalToPID(pid)
}
