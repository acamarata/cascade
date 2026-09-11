// Purpose: `cascade fleet sessions [--watch]` (07-CLI-COMMAND-TREE §fleet)
//
//	plus the hidden top-level alias `cascade sessions` registered from
//	root.go. One-shot mode dials fleet.sessions.list through the Go
//	client SDK (internal/client, D/S-07.T3) when a daemon is reachable,
//	and falls back to a live embedded census+state-machine read
//	(D/S-07.T4) otherwise — including on Windows tier-2, which has no
//	daemon at all. --watch subscribes to the daemon's GET
//	/events?topic=fleet.sessions SSE stream (D/S-06.T4) and is refused
//	(never panicked) when no daemon is reachable, or with a tier-2
//	explanation on Windows.
//
// Inputs: cobra args/flags; a fleetSessionsDeps injected at construction
//
//	so no test touches the real environment or a real socket (Art.7.1).
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure. Never a bare fmt.Print (internal/build's AST
//	output gate).
//
// Constraints: read-only verb. All rendering goes through
//
//	internal/output; no ad-hoc fmt.Print in the command handler body.
//
// CONTRACT DEVIATION (SSE client, recorded, not papered over). The
// contract's HOW step 3 says --watch "connects to the D/S-06.T4 SSE
// bridge client." internal/client's SSE half (stream.go's streamClient)
// is unexported by that file's own documented design (its whole exported
// surface is Client/Do/Status until a consuming command exists) and
// filters by job_id, not the topic query parameter
// internal/fleet/sessions.SSEHandler actually accepts. Exporting or
// reshaping stream.go is out of this ticket's files_scope (cmd/cascade
// only). fleetSessionsSSE below dials the same daemon socket through the
// SAME exported client.UnixDialer every other command uses, and parses
// the same WHATWG SSE grammar client/stream.go's own sseAccumulator
// proves against a fuzz corpus — a second small parser in cmd/cascade,
// not a second wire protocol.
//
// SPORT: cmd/cascade/fleet (ADD, per T-2 sport_updates).
package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// fleetSessionsDialTimeout bounds the one-shot round trip against the
// daemon, matching status.go's statusDialTimeout convention.
const fleetSessionsDialTimeout = 5 * time.Second

// fleetSessionsDeps carries every external input `cascade fleet sessions`
// needs, mirroring statusDeps's established injection pattern.
type fleetSessionsDeps struct {
	Paths       runtime.PathProvider
	Getenv      runtime.Getenv
	Environ     func() []string
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
}

// productionFleetSessionsDeps builds fleetSessionsDeps against the real
// environment.
func productionFleetSessionsDeps() fleetSessionsDeps {
	return fleetSessionsDeps{
		Paths:       lazyPaths{},
		Getenv:      os.Getenv,
		Environ:     os.Environ,
		DialContext: client.UnixDialer,
	}
}

// mountFleetCmd attaches the `fleet` command group, following
// mountStatusCmd's exact pattern.
func mountFleetCmd(root *cobra.Command) {
	deps := productionFleetSessionsDeps()
	cmd := newFleetCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
	mountFleetSessionsAlias(root)
	mountFleetJournalAlias(root, deps)
}

// mountFleetSessionsAlias registers the hidden top-level `cascade
// sessions` alias for `cascade fleet sessions` (07 §fleet consolidation
// note: "hidden top-level aliases kept: cascade top / sessions /
// attention"), called from root.go's mountSubcommands. Built as its own
// *cobra.Command (cobra has no built-in alias-to-another-subtree
// mechanism) sharing newFleetSessionsCmd's exact construction, so the two
// commands can never drift apart; Hidden=true keeps it out of --help
// while resolving identically.
func mountFleetSessionsAlias(root *cobra.Command) {
	cmd := newFleetSessionsCmd(productionFleetSessionsDeps())
	cmd.Use = "sessions"
	cmd.Hidden = true
	root.AddCommand(cmd)
}

// newFleetCmd builds the `fleet` command group.
func newFleetCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Inspect the local fleet of harness sessions",
	}
	cmd.AddCommand(newFleetSessionsCmd(deps))
	cmd.AddCommand(newFleetJournalCmd(deps))
	return cmd
}

// newFleetSessionsCmd builds `fleet sessions [--watch]`. The --json and
// --profile flags are the root's persistent global flags (07 §global-
// flags); only --watch is local to this command.
func newFleetSessionsCmd(deps fleetSessionsDeps) *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List fleet sessions (one-shot table/JSON, or --watch to stream changes)",
		Long: "List fleet sessions: session_id, binary, state, confidence, account, elapsed.\n" +
			"One-shot mode (default) works via the daemon or, when none is running,\n" +
			"an embedded read - including on Windows. --watch streams live updates and\n" +
			"needs a running daemon; its non-interactive equivalent (automation parity,\n" +
			"06 §5.8) is `cascade fleet sessions --json`.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch {
				return runFleetSessionsWatch(cmd, deps)
			}
			return runFleetSessionsOnce(cmd, deps)
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "stream live session changes (requires a running daemon)")
	return cmd
}

// runFleetSessionsOnce renders one-shot fleet.sessions.list output.
func runFleetSessionsOnce(cmd *cobra.Command, deps fleetSessionsDeps) error {
	rows, err := fetchFleetSessions(cmd.Context(), deps)
	if err != nil {
		return err
	}
	return fleetSessionsOutputWriter(cmd).Result(fleetSessionRows(rows))
}

// fleetSessionsOutputWriter mirrors statusOutputWriter's exact pattern.
func fleetSessionsOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// fleetSessionRow is the unified column set both the daemon path and the
// embedded path render into: session_id, binary, state, confidence,
// account, elapsed.
type fleetSessionRow struct {
	SessionID  string `json:"session_id"`
	Binary     string `json:"binary"`
	State      string `json:"state"`
	Confidence string `json:"confidence"`
	Account    string `json:"account"`
	Elapsed    string `json:"elapsed"`
}

// fleetSessionRows wraps []fleetSessionRow with a human table String().
type fleetSessionRows []fleetSessionRow

// String renders the table view.
func (rows fleetSessionRows) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SESSION_ID\tBINARY\tSTATE\tCONFIDENCE\tACCOUNT\tELAPSED\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.SessionID, r.Binary, r.State, r.Confidence, r.Account, r.Elapsed)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// fetchFleetSessions routes through the client when the daemonless probe
// confirms a live daemon, and through the embedded runtime otherwise,
// mirroring fetchContextSlice's own routing rule.
func fetchFleetSessions(ctx context.Context, deps fleetSessionsDeps) ([]fleetSessionRow, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if ok && !st.Embedded {
		return fetchFleetSessionsDaemon(ctx, deps)
	}
	return embeddedFleetSessionRows()
}

// fetchFleetSessionsDaemon calls fleet.sessions.list through
// internal/client.Client (which already satisfies sessions.RPCCaller),
// the ONLY way this file reaches the daemon.
func fetchFleetSessionsDaemon(ctx context.Context, deps fleetSessionsDeps) ([]fleetSessionRow, error) {
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetSessionsDialTimeout)
	records, err := sessions.NewClient(c).List(ctx, sessions.Filter{})
	if err != nil {
		return nil, err
	}
	rows := make([]fleetSessionRow, 0, len(records))
	for _, rec := range records {
		rows = append(rows, fleetSessionRow{
			SessionID: rec.SessionID,
			Binary:    rec.Harness,
			State:     rec.State,
			// CONTRACT DEVIATION (confidence, recorded): SessionRecord
			// (domain.go) carries no Confidence field and
			// fleet.sessions.list's wire shape does not send one - see
			// this file's header. The daemon path therefore cannot
			// report a live confidence score without re-deriving it
			// from signals this record does not carry.
			Confidence: "n/a",
			Account:    rec.Account,
			Elapsed:    formatElapsed(rec.UpdatedAt, runtime.NewSystemClock()),
		})
	}
	return rows, nil
}

// formatElapsed renders time elapsed since a unix-epoch timestamp, as
// measured by clk.Now() - never a bare time.Since (02-TARGET-
// STRUCTURE.md §v1.1, enforced by golangci's forbidigo rule).
func formatElapsed(updatedAt int64, clk runtime.Clock) string {
	if updatedAt <= 0 {
		return "-"
	}
	d := clk.Now().Sub(time.Unix(updatedAt, 0))
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// embeddedFleetSessionRows is the D/S-07.T4 embedded runtime path: a
// live, single-shot census scan classified through the S-25.T1 state
// machine directly, with no persisted domain record (nothing has been
// polling into the sessions Store without a daemon). This is why its
// session_id and elapsed columns differ in shape from the daemon path -
// recorded in this ticket's journal.
func embeddedFleetSessionRows() ([]fleetSessionRow, error) {
	snapshots, err := census.New(nil).Enumerate()
	if err != nil {
		return nil, err
	}
	machine := sessions.NewStateMachine()
	clock := runtime.NewSystemClock()
	rows := make([]fleetSessionRow, 0, len(snapshots))
	for _, snap := range snapshots {
		state, confidence := machine.Advance(sessions.Observation{Census: &snap}, clock)
		rows = append(rows, fleetSessionRow{
			SessionID:  fmt.Sprintf("pid:%d", snap.Pid),
			Binary:     snap.Binary,
			State:      state.String(),
			Confidence: fmt.Sprintf("%.2f", float64(confidence)),
			Account:    snap.Account,
			Elapsed:    "-",
		})
	}
	return rows, nil
}
