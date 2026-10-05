// Purpose: `cascade daemon logs [-f]` (07-CLI-COMMAND-TREE §daemon), the CLI
//
//	mount over runtime.DaemonLogsHandler. It prints the one log file every
//	daemon subsystem writes to; with -f it keeps following it until the
//	command is interrupted, across a log rotation.
//
// Inputs: daemonDeps.Paths (the log path comes from runtime.LogFilePath, so
//
//	no daemon has to be running) and the -f flag.
//
// Outputs: log content on the command's stdout, diagnostics (no log file
//
//	yet, file disappeared, rotated) on its stderr, exactly as the handler
//	emits them.
//
// Constraints: the handler follows ONE file and exits when the path stops
//
//	naming it (rotation renames the active file away and creates a fresh
//	one). Following across a rotation is therefore this file's loop: when
//	the handler returns while the command is still live and the path
//	resolves again, it runs again, and the new file is read from its start,
//	so every line the rotated-in file holds appears. A path that does not
//	resolve (deleted, or never created) ends the command. Each pass lasts
//	at least one poll interval, so the loop cannot spin.
//
// SPORT: cmd/cascade/daemon (CHANGE, daemon logs mount).
package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

// newDaemonLogsCmd builds `daemon logs` with the handler's default poll
// cadence.
func newDaemonLogsCmd(deps daemonDeps) *cobra.Command {
	return newDaemonLogsCmdPolled(deps, 0)
}

// newDaemonLogsCmdPolled is newDaemonLogsCmd with the follow poll interval
// injected; <=0 keeps the handler's default. Tests pass a short interval so
// following is observed in milliseconds rather than raced against 200ms.
func newDaemonLogsCmdPolled(deps daemonDeps, poll time.Duration) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print the daemon log file, optionally following it",
		Long: "Print the daemon log file. Needs no running daemon: the file is read\n" +
			"directly. With -f, keep streaming new lines (across a log rotation)\n" +
			"until interrupted.",
		Args: cobra.NoArgs,
		// No embedded-mode fallback exists for this verb (it only reads a
		// file), so announcing "running in embedded mode" would be noise.
		Annotations: map[string]string{embeddedWarningAnnotation: "suppress"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return streamDaemonLogs(cmd.Context(), logStream{
				path: runtime.LogFilePath(deps.Paths), follow: follow, poll: poll,
				out: cmd.OutOrStdout(), diag: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new lines until interrupted")
	return cmd
}

// logStream carries one `daemon logs` invocation's inputs.
type logStream struct {
	path   string
	follow bool
	poll   time.Duration
	out    io.Writer
	diag   io.Writer
}

// streamDaemonLogs runs the handler once, and in follow mode again each time
// it returns because the log was rotated, until ctx ends, the path no longer
// resolves, or the handler fails.
func streamDaemonLogs(ctx context.Context, s logStream) error {
	for {
		err := runtime.DaemonLogsHandler(ctx, runtime.DaemonLogsOptions{
			Path: s.path, Follow: s.follow, Out: s.out, Diag: s.diag, PollInterval: s.poll,
		})
		if err != nil || !s.follow || ctx.Err() != nil {
			return err
		}
		// The handler returned without being cancelled: the file it held
		// was rotated away or deleted. A path that resolves again is the
		// rotated-in file, so follow it; one that does not is gone.
		if _, statErr := os.Stat(s.path); statErr != nil {
			return nil
		}
	}
}
