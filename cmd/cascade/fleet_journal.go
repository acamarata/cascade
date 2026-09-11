// Purpose: `cascade fleet journal show|replay <entity>` (07-CLI-COMMAND-
//
//	TREE §fleet: "journal show|replay <entity> <- absorbs `cascade
//	journal` (S-27.T4)") plus the hidden top-level alias `cascade
//	journal` registered from mountFleetCmd (fleet.go). Both subcommands
//	dial fleet.journal_show/fleet.journal_replay through the Go client
//	SDK (internal/client via internal/fleet/journal.Client, D/S-07.T3) —
//	there is no embedded fallback: the journal is a daemon-owned durable
//	log, so an unreachable daemon is a plain refusal, never a
//	degraded-but-silent local read.
//
// Inputs: cobra args/flags; the SAME fleetSessionsDeps injection fleet.go
//
//	already established for daemon-dial commands.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure. Never a bare fmt.Print.
//
// Constraints: read-only verb. `journal show` never prints a value the
//
//	repo's existing redaction path (internal/doctor.RedactText, §D-31)
//	would flag as secret-shaped — applied to every entry's payload
//	before it is ever rendered, table or --json alike, so the redaction
//	happens once, upstream of both render paths, not per-format.
//
// CONTRACT DEVIATION (--stream, recorded, not papered over). See
// internal/fleet/journal/rpc.go's package doc comment for the full
// reasoning: no SSE topic exists for the journal domain, and journal
// replay is inherently a historical read (REPLAY IS NOT RE-EXECUTION),
// not a live subscription. --stream here renders the SAME batched
// fleet.journal_replay response as one NDJSON line per entry
// (internal/output's existing NDJSONWriter — the identical wire format
// fleet sessions --watch's own non-TTY path already uses) rather than
// dialing a live event stream.
//
// SPORT: cmd/cascade/fleet (CHANGE, per T-4 sport_updates: journal show|
//
//	replay CLI entity).
package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fleetJournalDialTimeout mirrors fleet.go's fleetSessionsDialTimeout.
const fleetJournalDialTimeout = 5 * time.Second

// errJournalNoDaemon is the refusal when no daemon socket is reachable:
// the journal has no embedded/offline equivalent (it is a daemon-owned
// durable log), so this is a plain, actionable refusal rather than a
// silent fallback to something less than the real log.
var errJournalNoDaemon = cascade.New(cascade.KindUnavailable,
	"cascade fleet journal: no daemon socket reachable; start it with `cascade daemon run`")

// errJournalNegativeSeq is the CLI-layer refusal for a negative --after/
// --from value, caught before ever building an RPC request.
var errJournalNegativeSeq = cascade.New(cascade.KindInvalidInput,
	"cascade fleet journal: sequence number must not be negative")

// mountFleetJournalAlias registers the hidden top-level `cascade journal`
// alias for `cascade fleet journal`, mirroring
// mountFleetSessionsAlias's exact pattern (fleet.go).
func mountFleetJournalAlias(root *cobra.Command, deps fleetSessionsDeps) {
	cmd := newFleetJournalCmd(deps)
	cmd.Use = "journal"
	cmd.Hidden = true
	root.AddCommand(cmd)
}

// newFleetJournalCmd builds the `journal` command group mounted under
// `fleet` (and, hidden, at the root).
func newFleetJournalCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Inspect an entity's fleet journal (show or replay)",
	}
	cmd.AddCommand(newFleetJournalShowCmd(deps))
	cmd.AddCommand(newFleetJournalReplayCmd(deps))
	return cmd
}

// newFleetJournalShowCmd builds `fleet journal show <entity> [--after N]
// [--limit N]`.
func newFleetJournalShowCmd(deps fleetSessionsDeps) *cobra.Command {
	var after int64
	var limit int
	cmd := &cobra.Command{
		Use:   "show <entity>",
		Short: "Show an entity's journal entries in sequence order",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			afterPtr, err := resolveSeqFlag(cmd, "after", after)
			if err != nil {
				return err
			}
			jc, err := dialFleetJournal(cmd.Context(), deps)
			if err != nil {
				return err
			}
			entries, err := jc.Show(cmd.Context(), args[0], afterPtr, limit)
			if err != nil {
				return err
			}
			return fleetJournalOutputWriter(cmd).Result(journalEntryRowsFrom(entries))
		},
	}
	cmd.Flags().Int64Var(&after, "after", 0, "only entries after this sequence number")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum entries to return (server-side clamp at 500)")
	return cmd
}

// newFleetJournalReplayCmd builds `fleet journal replay <entity> [--from
// N] [--stream]`.
func newFleetJournalReplayCmd(deps fleetSessionsDeps) *cobra.Command {
	var from int64
	var stream bool
	cmd := &cobra.Command{
		Use:   "replay <entity>",
		Short: "Re-emit an entity's journal entries in sequence order",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromPtr, err := resolveSeqFlag(cmd, "from", from)
			if err != nil {
				return err
			}
			jc, err := dialFleetJournal(cmd.Context(), deps)
			if err != nil {
				return err
			}
			entries, err := jc.Replay(cmd.Context(), args[0], fromPtr)
			if err != nil {
				return err
			}
			w := fleetJournalOutputWriter(cmd)
			if stream {
				return emitJournalNDJSON(w, entries)
			}
			return w.Result(journalEntryRowsFrom(entries))
		},
	}
	cmd.Flags().Int64Var(&from, "from", 0, "only entries after this sequence number")
	cmd.Flags().BoolVar(&stream, "stream", false, "emit one NDJSON line per entry instead of a batched result")
	return cmd
}

// resolveSeqFlag converts a cobra int64 flag into a *uint64 cursor value,
// nil when the flag was never set (Changed reports false), and a typed
// error for an explicitly negative value — the "negative --after seq"
// acceptance case, caught here rather than ever reaching the RPC layer.
func resolveSeqFlag(cmd *cobra.Command, name string, value int64) (*uint64, error) {
	if !cmd.Flags().Changed(name) {
		return nil, nil
	}
	if value < 0 {
		return nil, errJournalNegativeSeq
	}
	v := uint64(value)
	return &v, nil
}

// dialFleetJournal builds a journal.Client dialing the daemon, refusing
// with errJournalNoDaemon when the daemonless probe found no reachable
// socket.
func dialFleetJournal(ctx context.Context, deps fleetSessionsDeps) (*journal.Client, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return nil, errJournalNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetJournalDialTimeout)
	return journal.NewClient(c), nil
}

// fleetJournalOutputWriter mirrors fleetSessionsOutputWriter's exact
// pattern.
func fleetJournalOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// journalEntryRow is one rendered journal entry: seq/kind/operation_id/
// timestamp/payload, with payload passed through internal/doctor's
// existing §D-31 redaction pass before it is ever rendered — table or
// --json, this is the one place either path builds a row from.
type journalEntryRow struct {
	Seq         uint64 `json:"seq"`
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	Timestamp   string `json:"timestamp"`
	Payload     string `json:"payload"`
}

// newJournalEntryRow builds one row from a real journal.Entry, redacting
// its payload before render.
func newJournalEntryRow(e journal.Entry) journalEntryRow {
	payload := string(e.Payload)
	if payload == "" {
		payload = "-"
	} else {
		payload = doctor.RedactText(payload)
	}
	return journalEntryRow{
		Seq:         e.Seq,
		Kind:        e.Kind.String(),
		OperationID: e.OperationID,
		Timestamp:   e.Time().Format(time.RFC3339Nano),
		Payload:     payload,
	}
}

// journalEntryRows wraps []journalEntryRow with a human table String(),
// mirroring fleetSessionRows's exact pattern.
type journalEntryRows []journalEntryRow

func journalEntryRowsFrom(entries []journal.Entry) journalEntryRows {
	rows := make(journalEntryRows, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, newJournalEntryRow(e))
	}
	return rows
}

// String renders the table view.
func (rows journalEntryRows) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SEQ\tKIND\tOPERATION_ID\tTIMESTAMP\tPAYLOAD\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", r.Seq, r.Kind, r.OperationID, r.Timestamp, r.Payload)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// emitJournalNDJSON renders entries as one NDJSON line per entry (the
// --stream rendering; see this file's CONTRACT DEVIATION note).
func emitJournalNDJSON(w *output.Writer, entries []journal.Entry) error {
	nd := w.NDJSON()
	for _, e := range entries {
		if err := nd.Emit(newJournalEntryRow(e)); err != nil {
			return err
		}
	}
	return nil
}
