//go:build !windows

// Purpose: additional unit-lane coverage for `cascade fleet journal`'s
//
//	dial-adjacent and output-adjacent code fleet_journal_test.go's original
//	suite left at 0%: dialFleetJournal's real dial-failure branch (past the
//	no-daemon refusal, mirroring fleet_coverage_test.go's established
//	technique for fleet sessions), fleetJournalOutputWriter's real Result
//	render, and emitJournalNDJSON's --stream rendering, including its
//	propagated-error branch on a write failure.
//
// SPORT: cmd/cascade/fleet (CHANGE, coverage-only).
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// newHermeticJournalDeps mirrors fleet_coverage_test.go's
// newHermeticFleetDeps: real production dial closure, socket path under a
// fresh temp dir nothing listens on.
func newHermeticJournalDeps(t *testing.T) fleetSessionsDeps {
	t.Helper()
	dir := t.TempDir()
	deps := productionFleetSessionsDeps()
	deps.Paths = fakeDaemonPaths{root: dir}
	return deps
}

// TestDialFleetJournal_DialFails proves that once the daemonless probe
// confirms a live daemon (Embedded=false), dialFleetJournal proceeds past
// errJournalNoDaemon into real settings resolution and client construction,
// surfacing the real dial failure against a socket nothing listens on -
// never a panic, never a silent embedded fallback (the journal has none).
func TestDialFleetJournal_DialFails(t *testing.T) {
	deps := newHermeticJournalDeps(t)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	jc, err := dialFleetJournal(ctx, deps)
	if err != nil {
		t.Fatalf("dialFleetJournal: unexpected construction error: %v", err)
	}
	if _, err := jc.Show(ctx, "some-entity", nil, 0); err == nil {
		t.Fatal("journal.Client.Show: expected a dial error against a socket nothing listens on")
	}
}

// newTestJournalCmd registers the four global output flags locally so
// fleetJournalOutputWriter can read them without a root mount, mirroring
// newTestFleetCmd's exact pattern.
func newTestJournalCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "show"}
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

// TestFleetJournalOutputWriter_RendersTable drives fleetJournalOutputWriter
// end to end: reads the four global flags off a real cobra.Command and
// renders a real journalEntryRows table through it.
func TestFleetJournalOutputWriter_RendersTable(t *testing.T) {
	cmd, buf := newTestJournalCmd()
	rows := journalEntryRowsFrom([]journal.Entry{
		{Seq: 1, Kind: journal.KindIntent, OperationID: "op1"},
	})
	if err := fleetJournalOutputWriter(cmd).Result(rows); err != nil {
		t.Fatalf("fleetJournalOutputWriter.Result: unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "op1") {
		t.Errorf("output missing rendered row: %q", buf.String())
	}
}

// TestFleetJournalOutputWriter_RendersJSON proves the --json branch reads
// the same flag set into the JSON envelope path.
func TestFleetJournalOutputWriter_RendersJSON(t *testing.T) {
	cmd, buf := newTestJournalCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	rows := journalEntryRowsFrom([]journal.Entry{{Seq: 2, Kind: journal.KindAck, OperationID: "op2"}})
	if err := fleetJournalOutputWriter(cmd).Result(rows); err != nil {
		t.Fatalf("fleetJournalOutputWriter.Result: unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), `"operation_id": "op2"`) {
		t.Errorf("--json output missing rendered field: %q", buf.String())
	}
}

// TestEmitJournalNDJSON_EmitsOneLinePerEntry proves `--stream` renders one
// NDJSON line per entry rather than a single batched result.
func TestEmitJournalNDJSON_EmitsOneLinePerEntry(t *testing.T) {
	buf := &bytes.Buffer{}
	w := output.New(buf, &bytes.Buffer{}, false, false, false, true)
	entries := []journal.Entry{
		{Seq: 1, Kind: journal.KindIntent, OperationID: "op1"},
		{Seq: 2, Kind: journal.KindAck, OperationID: "op2"},
	}
	if err := emitJournalNDJSON(w, entries); err != nil {
		t.Fatalf("emitJournalNDJSON: unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d NDJSON lines, want 2: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "op1") || !strings.Contains(lines[1], "op2") {
		t.Errorf("NDJSON lines missing expected operation ids: %v", lines)
	}
}

// TestEmitJournalNDJSON_EmptyResultSetEmitsNothing proves an empty entry
// slice - the "empty result set" acceptance case - emits zero lines and no
// error, never a panic on an empty range.
func TestEmitJournalNDJSON_EmptyResultSetEmitsNothing(t *testing.T) {
	buf := &bytes.Buffer{}
	w := output.New(buf, &bytes.Buffer{}, false, false, false, true)
	if err := emitJournalNDJSON(w, nil); err != nil {
		t.Fatalf("emitJournalNDJSON(nil): unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for an empty result set, got %q", buf.String())
	}
}

// failingWriter always errors on Write, so emitJournalNDJSON's propagated
// write-failure branch (an unparseable/undeliverable row downstream) is
// exercised rather than assumed.
type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) { return 0, errors.New("write: broken pipe") }

// TestEmitJournalNDJSON_WriteFailurePropagates proves a write failure on
// entry N stops the loop and surfaces the real error rather than being
// swallowed.
func TestEmitJournalNDJSON_WriteFailurePropagates(t *testing.T) {
	w := output.New(failingWriter{}, &bytes.Buffer{}, false, false, false, true)
	err := emitJournalNDJSON(w, []journal.Entry{{Seq: 1, Kind: journal.KindIntent}})
	if err == nil {
		t.Fatal("emitJournalNDJSON: expected the underlying write failure to propagate")
	}
}
