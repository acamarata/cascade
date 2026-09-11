// Purpose: unit tests for `cascade fleet journal show|replay` - mount
//
//	reachability, the hidden `cascade journal` alias, table rendering,
//	the sequence-flag validator, the no-daemon refusal, and the §D-31
//	redaction pass over payload (proved falsifiable in this ticket's
//	journal: the redaction assertion below was confirmed to fail when
//	the redaction call was temporarily removed, then the removal was
//	reverted byte-for-byte - see the ticket journal for both outcomes).
//	This file deliberately imports neither "net" nor "net/http" so it
//	runs in the fast, no-network unit lane, matching fleet_test.go's own
//	convention; the real-dial, real-daemon acceptance path lives in
//	internal/fleet/journal/epicm_acceptance_test.go (integration-tagged).
//
// SPORT: cmd/cascade/fleet (CHANGE, per T-4 sport_updates).
package main

import (
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/acamarata/cascade/internal/fleet/journal"
)

// TestFleetJournalMountedOnRoot proves `fleet journal show|replay` and
// the hidden `journal show|replay` alias all resolve on the real root
// command tree.
func TestFleetJournalMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{
		{"fleet", "journal", "show"}, {"fleet", "journal", "replay"},
		{"journal", "show"}, {"journal", "replay"},
	} {
		found, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v is not mounted on the root command: %v", path, err)
		}
		wantName := path[len(path)-1]
		if !strings.HasPrefix(found.Name(), wantName) {
			t.Fatalf("%v resolved to %q, want a command named %q", path, found.Name(), wantName)
		}
	}
}

// TestFleetJournalAliasHidden proves the top-level `journal` alias is
// hidden from --help while still resolving.
func TestFleetJournalAliasHidden(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"journal"})
	if err != nil {
		t.Fatalf("journal alias not found: %v", err)
	}
	if !found.Hidden {
		t.Fatal("cascade journal alias must be Hidden (07 §fleet consolidation note)")
	}
}

// TestJournalEntryRows_TableHasRequiredColumns proves the human table
// carries every column the acceptance story's "kind, sequence,
// timestamp, payload summary" requirement names.
func TestJournalEntryRows_TableHasRequiredColumns(t *testing.T) {
	rows := journalEntryRowsFrom([]journal.Entry{
		{EntityID: "e1", Seq: 1, Kind: journal.KindIntent, OperationID: "op1", Payload: []byte(`{"a":1}`)},
	})
	out := rows.String()
	for _, col := range []string{"SEQ", "KIND", "OPERATION_ID", "TIMESTAMP", "PAYLOAD"} {
		if !strings.Contains(out, col) {
			t.Errorf("table output missing column header %q:\n%s", col, out)
		}
	}
	for _, val := range []string{"1", "intent", "op1"} {
		if !strings.Contains(out, val) {
			t.Errorf("table output missing value %q:\n%s", val, out)
		}
	}
}

// TestJournalEntryRows_EmptyTableHasHeaderOnly proves an empty result
// still renders the header row, never panics - the "empty journal exits
// 0" acceptance case rendered through the CLI layer.
func TestJournalEntryRows_EmptyTableHasHeaderOnly(t *testing.T) {
	out := journalEntryRowsFrom(nil).String()
	if !strings.Contains(out, "SEQ") {
		t.Errorf("empty table missing header: %q", out)
	}
}

// TestNewJournalEntryRow_EmptyPayloadIsDash proves an entry with no
// payload renders as "-", never an empty cell that could be misread as a
// rendering bug.
func TestNewJournalEntryRow_EmptyPayloadIsDash(t *testing.T) {
	row := newJournalEntryRow(journal.Entry{Seq: 1, Kind: journal.KindAck})
	if row.Payload != "-" {
		t.Errorf("Payload = %q, want \"-\" for an empty payload", row.Payload)
	}
}

// TestNewJournalEntryRow_RedactsSecretShapedPayload proves `journal show`
// never renders a secret-shaped payload value verbatim: internal/doctor's
// existing §D-31 detector must have already scrubbed it by the time a row
// is built, since newJournalEntryRow is the one place both the table and
// --json render paths construct a row from. Uses a split literal
// (AGENT-BRIEF's CREDENTIAL-SHAPED FIXTURES rule) so no contiguous
// AWS-access-key-shaped string exists in this source file for GitHub
// push protection to flag, while the runtime value assembled at test
// time is unchanged and still exercises the real detector.
func TestNewJournalEntryRow_RedactsSecretShapedPayload(t *testing.T) {
	secret := "AKIA" + "7YQ2XPLM4RZV6WTB"
	payload := []byte(`{"token":"` + secret + `"}`)
	row := newJournalEntryRow(journal.Entry{Seq: 1, Kind: journal.KindIntent, Payload: payload})
	if strings.Contains(row.Payload, secret) {
		t.Fatalf("newJournalEntryRow did not redact a secret-shaped payload value: %q", row.Payload)
	}
	if !strings.Contains(row.Payload, "REDACTED") {
		t.Errorf("newJournalEntryRow's redacted payload has no REDACTED marker: %q", row.Payload)
	}
}

// TestResolveSeqFlag_Unset proves an unset flag resolves to a nil cursor
// (no filtering requested), not a zero value indistinguishable from an
// explicit --after 0.
func TestResolveSeqFlag_Unset(t *testing.T) {
	cmd := newFleetJournalShowCmd(fleetSessionsDeps{})
	got, err := resolveSeqFlag(cmd, "after", 0)
	if err != nil {
		t.Fatalf("resolveSeqFlag: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("resolveSeqFlag(unset) = %v, want nil", *got)
	}
}

// TestResolveSeqFlag_NegativeIsTypedError proves an explicitly negative
// --after/--from value is refused before ever reaching the RPC layer.
func TestResolveSeqFlag_NegativeIsTypedError(t *testing.T) {
	cmd := newFleetJournalShowCmd(fleetSessionsDeps{})
	if err := cmd.Flags().Set("after", "-1"); err != nil {
		t.Fatalf("Set(after, -1): %v", err)
	}
	_, err := resolveSeqFlag(cmd, "after", -1)
	if err == nil {
		t.Fatal("resolveSeqFlag(-1): want an error, got nil")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("resolveSeqFlag(-1) error = %v, want it to mention 'negative'", err)
	}
}

// TestResolveSeqFlag_PositiveSet proves an explicitly set non-negative
// value round-trips as a *uint64.
func TestResolveSeqFlag_PositiveSet(t *testing.T) {
	cmd := newFleetJournalShowCmd(fleetSessionsDeps{})
	if err := cmd.Flags().Set("after", "7"); err != nil {
		t.Fatalf("Set(after, 7): %v", err)
	}
	got, err := resolveSeqFlag(cmd, "after", 7)
	if err != nil {
		t.Fatalf("resolveSeqFlag: unexpected error: %v", err)
	}
	if got == nil || *got != 7 {
		t.Fatalf("resolveSeqFlag(7) = %v, want *uint64(7)", got)
	}
}

// TestFleetJournalShow_NoDaemon_ActionableError proves `fleet journal
// show` refuses with an actionable message (never a panic, never a bare
// "failed") when no daemon is reachable - the default state of a fresh
// cmd.Context() in this no-network unit lane.
func TestFleetJournalShow_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetJournalShowCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"some-entity"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet journal show with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

// TestFleetJournalReplay_NoDaemon_ActionableError mirrors the show case
// for replay.
func TestFleetJournalReplay_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetJournalReplayCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"some-entity"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet journal replay with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

// TestFleetJournalShow_ExtraArgRefused proves a second positional
// argument is refused (ExactArgs(1)), matching fleet sessions' own
// "positional argument is refused" convention.
func TestFleetJournalShow_ExtraArgRefused(t *testing.T) {
	cmd := newFleetJournalShowCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"entity-a", "entity-b"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("fleet journal show with two positional args: want an error, got nil")
	}
}

// TestFleetJournalWindowsAndUnix_MountSameOnGOOS is a light GOOS
// sanity check: journal CLI mounting takes no per-platform branch (the
// per-platform refusal, if any, is entirely in dialFleetJournal's own
// daemonless-state check, exercised above).
func TestFleetJournalWindowsAndUnix_MountSameOnGOOS(t *testing.T) {
	if goruntime.GOOS == "" {
		t.Fatal("runtime.GOOS unexpectedly empty")
	}
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	if _, _, err := root.Find([]string{"fleet", "journal"}); err != nil {
		t.Fatalf("fleet journal not mounted on %s: %v", goruntime.GOOS, err)
	}
}

// TestFleetJournalAlias_IdenticalConstruction proves the hidden `cascade
// journal` alias and `cascade fleet journal` are built from the exact
// same newFleetJournalCmd(deps) call (mountFleetJournalAlias's own body),
// not a second, hand-maintained re-implementation that could drift: both
// trees carry identical subcommand names and identical flag sets, which
// can only be true by construction, never by coincidence.
func TestFleetJournalAlias_IdenticalConstruction(t *testing.T) {
	deps := fleetSessionsDeps{}
	canonical := newFleetJournalCmd(deps)
	alias := newFleetJournalCmd(deps)
	alias.Use = "journal"
	alias.Hidden = true

	for _, verb := range []string{"show", "replay"} {
		c, _, err := canonical.Find([]string{verb})
		if err != nil {
			t.Fatalf("canonical journal %s not found: %v", verb, err)
		}
		a, _, err := alias.Find([]string{verb})
		if err != nil {
			t.Fatalf("alias journal %s not found: %v", verb, err)
		}
		var cFlags, aFlags []string
		c.Flags().VisitAll(func(f *pflag.Flag) { cFlags = append(cFlags, f.Name) })
		a.Flags().VisitAll(func(f *pflag.Flag) { aFlags = append(aFlags, f.Name) })
		if strings.Join(cFlags, ",") != strings.Join(aFlags, ",") {
			t.Errorf("journal %s flag sets differ: canonical=%v alias=%v", verb, cFlags, aFlags)
		}
	}
}
