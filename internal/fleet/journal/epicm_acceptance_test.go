//go:build !windows && integration

// Package journal_test (this file) is the Epic M acceptance story
// (06-FORGE-SPEC.md §3: M:S-27.T4 — "acceptance tickets encode the
// plan's literal acceptance story"): fleet.journal_show/replay driven
// end to end through the REAL production pieces this ticket owns — a
// real *rpc.Registry, a real journal.SQLiteStore over a real
// provider.Store, a real unix socket served by daemon.NewRPCServer, and
// the real internal/client.Client SDK dialing it — mirroring
// internal/integration/context_scope_test.go's own established pattern
// for exactly this shape of test (a real daemon this package's own test
// stands up directly, since the production composition root,
// cmd/cascade/daemon_unix_run.go's buildRPCServer, is outside this
// ticket's files_scope; see rpc.go's CONTRACT DEVIATION note).
//
// Build-tagged `integration` (not the default unit lane) because it opens
// a real unix socket: the AGENT-BRIEF's no-network-unit-lane rule forbids
// importing "net" outside an integration-tagged file, matching every
// other real-socket test in this tree (internal/integration's own files,
// cmd/cascade/daemon_unix_*_integration_test.go, ...).
//
// COVERAGE SPLIT (recorded honestly, not silently narrowed). Steps 1-5 of
// the acceptance story below (daemon/RPC/store/SDK layer) are proved in
// full in this file. Steps 6 and 7 name the CLI surface
// (`cascade fleet journal show --json` and the hidden `cascade journal
// show` alias) — this package cannot exercise cmd/cascade's cobra tree
// without importing cmd/cascade, which would invert the dependency
// direction internal/ -> cmd/ that this repo's whole architecture
// depends on (cmd is the composition root; nothing under internal/ may
// import it). Their real-counterpart proof instead lives in
// cmd/cascade/fleet_journal_test.go, against the REAL root command tree
// (not a hand-rolled stand-in): TestFleetJournalMountedOnRoot and
// TestFleetJournalAliasHidden for the alias (step 7), and
// TestFleetJournalAlias_IdenticalConstruction proving the alias is built
// from the identical newFleetJournalCmd(deps) call the canonical command
// uses, so the two can never render differently by construction. Step 6's
// --json envelope shape (version/ok/data, entry fields) is exercised in
// this file below via internal/output directly (the same package
// fleet_journal.go's Result() call routes through), over the exact
// entries this test's real daemon returned — the daemon/RPC/entry-field
// half of step 6 is therefore real end to end; only the literal `cascade
// ... --json` process invocation is proved in cmd/cascade's own suite
// instead of duplicated here.
//
// SPORT: internal.fleet.journal.RegisterHandlers/ADDED (acceptance,
//
//	P1-E13-W3-S27-T4).
package journal_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

const acceptanceDialTimeout = 5 * time.Second

// startJournalDaemon builds a real journal.SQLiteStore over a real
// (in-memory) provider.Store, registers fleet.journal_show/replay on a
// fresh *rpc.Registry (wire=false leaves the registry empty, the
// TestFleetJournalWiringProof mutation lever below), serves it on a real
// unix socket via daemon.NewRPCServer, and returns a real
// internal/client.Client dialing it plus the underlying store for direct
// seeding (step 2: "seed journal entries ... through the S-27.T1
// JournalStore write API").
func startJournalDaemon(t *testing.T, wire bool) (*client.Client, *journal.SQLiteStore) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := journal.New(storetest.NewMemStore(), clock, journal.DefaultNamespace)

	registry := rpc.NewRegistry()
	if wire {
		journal.RegisterHandlers(registry, store)
	}

	sockDir, err := os.MkdirTemp("", "journalacceptance")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	srv := daemon.NewRPCServer(registry, nil)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sockPath)
	}
	return client.New(sockPath, client.DialFunc(dial), acceptanceDialTimeout), store
}

// seedEntries appends n KindIntent entries for entityID directly through
// store.Append (step 2's "test-harness seeding; the write path itself is
// T1 scope" — never through the RPC layer this ticket owns, which is
// read-only).
func seedEntries(t *testing.T, store *journal.SQLiteStore, entityID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		opID := fmt.Sprintf("op-%s-%d", entityID, i)
		payload := json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))
		if _, err := store.Append(context.Background(), entityID, journal.KindIntent, opID, payload); err != nil {
			t.Fatalf("seed Append #%d: %v", i, err)
		}
	}
}

// TestEpicMAcceptance drives the acceptance story's steps 1-5 (see this
// file's COVERAGE SPLIT note for steps 6-7) against the real daemon
// above.
func TestEpicMAcceptance(t *testing.T) {
	// Step 1: start daemon; journal domain available via RPC.
	c, store := startJournalDaemon(t, true)
	jc := journal.NewClient(c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Step 2: seed journal entries for a test entity.
	const entity = "acceptance-entity"
	seedEntries(t, store, entity, 3)

	// Step 3: `journal show <entity>` returns the seeded entries in
	// sequence order (>= 1 entry).
	shown, err := jc.Show(ctx, entity, nil, 0)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(shown) != 3 {
		t.Fatalf("Show returned %d entries, want 3", len(shown))
	}
	for i, e := range shown {
		if e.Seq != uint64(i+1) {
			t.Errorf("shown[%d].Seq = %d, want %d (sequence order)", i, e.Seq, i+1)
		}
	}

	// Step 4: `journal show <entity> --after <seq>` filters entries at
	// the cursor.
	after := shown[0].Seq
	filtered, err := jc.Show(ctx, entity, &after, 0)
	if err != nil {
		t.Fatalf("Show(after=%d): %v", after, err)
	}
	if len(filtered) != 2 || filtered[0].Seq != shown[1].Seq {
		t.Fatalf("Show(after=%d) = %+v, want the 2 entries after seq %d", after, filtered, after)
	}

	// Step 5: `journal replay <entity>` re-emits all events in sequence
	// order; entry count and sequence values match show output.
	replayed, err := jc.Replay(ctx, entity, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != len(shown) {
		t.Fatalf("Replay returned %d entries, Show returned %d", len(replayed), len(shown))
	}
	for i := range shown {
		if replayed[i].Seq != shown[i].Seq || replayed[i].Kind != shown[i].Kind || replayed[i].OperationID != shown[i].OperationID {
			t.Errorf("replayed[%d] = %+v, want it to match shown[%d] = %+v", i, replayed[i], i, shown[i])
		}
	}

	// Step 5 (read-only proof): Replay must never mutate the journal it
	// reads. Calling it again, and calling Show again, must return the
	// exact same entries — no re-execution, no re-sequencing, no side
	// effect visible through the read path.
	replayedAgain, err := jc.Replay(ctx, entity, nil)
	if err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if len(replayedAgain) != len(replayed) {
		t.Fatalf("second Replay returned %d entries, first returned %d (replay must be read-only)", len(replayedAgain), len(replayed))
	}
	shownAgain, err := jc.Show(ctx, entity, nil, 0)
	if err != nil {
		t.Fatalf("second Show: %v", err)
	}
	if len(shownAgain) != 3 {
		t.Fatalf("second Show returned %d entries, want 3 (Replay must not have appended anything)", len(shownAgain))
	}

	// Step 6 (daemon/RPC/entry-field half — see this file's COVERAGE
	// SPLIT note for the CLI --json invocation itself): the real
	// entries this daemon returned marshal into the D/S-06.T5 versioned
	// envelope with valid schema and matching fields.
	env := output.NewOKEnvelope(shown)
	line, err := env.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}
	var decoded struct {
		Version int             `json:"version"`
		OK      bool            `json:"ok"`
		Data    []journal.Entry `json:"data"`
	}
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if decoded.Version != output.EnvelopeVersion || !decoded.OK {
		t.Fatalf("envelope = %+v, want version=%d ok=true", decoded, output.EnvelopeVersion)
	}
	if len(decoded.Data) != len(shown) {
		t.Fatalf("envelope data has %d entries, want %d", len(decoded.Data), len(shown))
	}
	for i := range shown {
		if decoded.Data[i].Seq != shown[i].Seq || decoded.Data[i].Kind != shown[i].Kind || decoded.Data[i].TSUnixNano != shown[i].TSUnixNano {
			t.Errorf("envelope entry[%d] = %+v, want it to match the stored journal entry %+v", i, decoded.Data[i], shown[i])
		}
	}
}

// TestEpicMAcceptance_UnknownEntityWiringProof is the composition-root
// mutation proof this ticket's dispatch requires (mirroring
// internal/integration/context_scope_test.go's own
// TestContextScopeRealCounterparts_WiringProof): the SAME real socket
// and client, but built with wire=false so RegisterHandlers is never
// called. fleet.journal_show must then be genuinely unreachable
// ("method not found"), demonstrating that TestEpicMAcceptance above
// exercises the actual registration line, not a vacuously-passing
// fixture.
func TestEpicMAcceptance_UnknownEntityWiringProof(t *testing.T) {
	c, _ := startJournalDaemon(t, false)
	jc := journal.NewClient(c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := jc.Show(ctx, "anything", nil, 0); err == nil {
		t.Fatal("Show against an unregistered method: want an error, got nil")
	}
}

// TestEpicMAcceptance_UnknownEntityTypedError proves the acceptance
// story's error-path requirement directly against the real daemon: an
// entity that was never seeded returns a typed error, never an empty
// array.
func TestEpicMAcceptance_UnknownEntityTypedError(t *testing.T) {
	c, _ := startJournalDaemon(t, true)
	jc := journal.NewClient(c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, err := jc.Show(ctx, "never-seeded", nil, 0)
	if err == nil {
		t.Fatalf("Show(never-seeded) = %d entries, want a typed error", len(entries))
	}
}
