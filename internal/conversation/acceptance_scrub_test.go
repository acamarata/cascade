package conversation

// Purpose: TestAcceptanceScrubInvariant -- S-44.T5 acceptance criterion 2.
//   Injects a real H/S-15.T3-grammar typed-secret sentinel (the raw
//   canary from testdata/v1-goldens/scrub/single_span.golden -- the same
//   golden corpus scrub_golden_test.go loads, not a hand-typed literal)
//   into a turn through the REAL chat.append_turn RPC path, over the REAL
//   scrub pipeline (newTestScrubPipeline, scrub_test.go: real Detector,
//   real QuarantineStore, real vault Broker over a ForceFileVault custody,
//   real Rewriter -- never a double of any of the four), and asserts the
//   raw sentinel is absent from two storage/transport layers read
//   independently of the scrub abstraction itself:
//     (a) SQLite storage -- a literal SQL SELECT against the same *sql.DB
//         the store writes through, not Store.ListSegments (which is
//         still the scrub-abstraction's own read path in spirit; the raw
//         column read is the more literal "storage read path" the AC
//         names).
//     (b) the SSE event payload bytes -- read via the real events.Bus's
//         own Replay (bus.go), the exact same Payload []byte
//         rpc.NewSSEHandler streams as each "data:" line (proven real
//         end to end by TestAcceptanceLiveMirror in this same acceptance
//         suite); this test's own concern is CONTENT, not transport, so
//         it reads the bus directly rather than re-opening a live socket
//         a sibling test already exercises.
//   The named failing input for both (a) and (b) is
//   golden.Canaries[0] == "sk-Canary0000AAAA1111BBBB2222CCCC3333" (see
//   testdata/v1-goldens/scrub/single_span.golden) -- a scrub-pipeline
//   regression that stops rewriting (e.g. scrub_phases.go's rewritePhase
//   skipped, or SetScrub never wired) makes this exact string appear in
//   both the raw SQL row and the raw SSE payload, and either assertion
//   below fails loudly naming it.
//
// FALSE PREMISE / CONFIRMED GAP (declared, not papered over): the
// ticket's sub-check (c) asks for "(c) stdout of a cascade chat replay
// command". No such command exists anywhere in this tree: `grep -rli
// replay cmd/ plugins/ docs/cli-reference/` returns no chat-related file,
// and docs/cli-reference/chat.md documents, as of this ticket, that
// `cascade chat` "does not answer" -- every invocation with a prompt
// returns errReplyGenerationUnavailable after recording the turn (no
// daemon component generates an assistant reply yet, internal/plugins/
// cascadepa_wiring.go), and its ONLY stdout path (writeOneShotResult,
// plugins/cascade-pa/cmd/chat.go) prints res.Content -- the assistant's
// generated reply -- never the raw or scrubbed stored turn content. There
// is therefore no CLI command whose stdout could leak or fail to leak the
// sentinel at all: this is a real, confirmed, pre-existing absence (not
// this ticket's regression, and not fixable inside this ticket's
// internal/conversation-only files_scope), so the sub-check is an
// explicit t.Skip naming exactly this, per the standing acceptance rule
// that an unprovable invariant must skip loudly, never pass silently.
// SPORT: internal.conversation/acceptance (ADDED, tests-only) (P1-E20-W5-S44-T5).

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// acceptanceScrubSetup wires a real scrub pipeline onto a real Adapter and
// appends one scrubbable turn through the REAL chat.append_turn RPC path,
// returning everything the subtests below assert against. Split out of
// TestAcceptanceScrubInvariant (Art.10.3: functions <=50 lines).
func acceptanceScrubSetup(t *testing.T) (db *sql.DB, bus *events.Bus, turnID, sentinel string, golden scrubGolden, vaultDir string) {
	t.Helper()
	golden = loadScrubGolden(t, "single_span.golden")
	sentinel = golden.Canaries[0] // "sk-Canary0000AAAA1111BBBB2222CCCC3333" -- the named failing input.

	store, storeDB := newTestStoreWithDB(t)
	realBus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	pipeline, _, dir := newTestScrubPipeline(t, realBus)

	adapter := NewAdapter(store, realBus, passthroughSubst{}, newAdapterTestClock(), "")
	adapter.SetScrub(pipeline)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	result, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th-accept-scrub", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: golden.Input}},
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn with a scrubbable turn: %+v", errObj)
	}
	return storeDB, realBus, result.(appendTurnResult).TurnID, sentinel, golden, dir
}

// acceptanceScrubAssertSQLiteBytes reads the stored segment content via a
// literal SQL SELECT against the same *sql.DB the store writes through --
// the raw column read the AC's own "storage read path" names.
func acceptanceScrubAssertSQLiteBytes(t *testing.T, db *sql.DB, turnID, sentinel, wantContent string) {
	t.Helper()
	var stored string
	if err := db.QueryRowContext(context.Background(),
		`SELECT content FROM `+tableSegment+` WHERE turn_id = ?`, turnID).Scan(&stored); err != nil {
		t.Fatalf("raw SQL read of conversation_segment.content: %v", err)
	}
	if stored != wantContent {
		t.Fatalf("stored segment content = %q, want the fixture's expected_output %q", stored, wantContent)
	}
	if containsSentinel(stored, sentinel) {
		t.Fatalf("SQLite storage carries the raw sentinel %q in column content: %q", sentinel, stored)
	}
}

// acceptanceScrubAssertSSEPayload reads the SSE event payload bytes via
// the real events.Bus's own Replay, the exact same Payload []byte
// rpc.NewSSEHandler streams as each "data:" line.
func acceptanceScrubAssertSSEPayload(t *testing.T, bus *events.Bus, sentinel, wantContent string) {
	t.Helper()
	evs, err := bus.Replay(context.Background(), turnAppendedNamespace, 0)
	if err != nil {
		t.Fatalf("bus.Replay(%q, 0): %v", turnAppendedNamespace, err)
	}
	if len(evs) != 1 {
		t.Fatalf("bus published %d conversation.turn_appended events, want 1", len(evs))
	}
	raw := evs[0].Payload
	if containsSentinel(string(raw), sentinel) {
		t.Fatalf("raw SSE event payload bytes carry the sentinel %q: %s", sentinel, raw)
	}
	var echoed turnAppendedPayload
	if err := json.Unmarshal(raw, &echoed); err != nil {
		t.Fatalf("decode SSE payload: %v", err)
	}
	if len(echoed.Segments) != 1 || echoed.Segments[0].Content != wantContent {
		t.Fatalf("SSE payload segment content = %+v, want %q", echoed.Segments, wantContent)
	}
}

// acceptanceScrubAssertVaultTag confirms the H/S-15.T4 vault-key
// reference placeholder, not the raw secret, is what storage carries.
func acceptanceScrubAssertVaultTag(t *testing.T, db *sql.DB, turnID, wantTag string) {
	t.Helper()
	var stored string
	if err := db.QueryRowContext(context.Background(),
		`SELECT content FROM `+tableSegment+` WHERE turn_id = ?`, turnID).Scan(&stored); err != nil {
		t.Fatalf("raw SQL read: %v", err)
	}
	if wantTag == "" || !containsSentinel(stored, wantTag) {
		t.Fatalf("stored content = %q, want it to contain the H/S-15.T4 vault-key reference placeholder %q",
			stored, wantTag)
	}
}

// acceptanceScrubAssertVaultHolds confirms the vault, not just the tag, is
// the source of truth this invariant depends on.
func acceptanceScrubAssertVaultHolds(t *testing.T, vaultDir, sentinel string) {
	t.Helper()
	got := string(verifyVaulted(t, vaultDir, "OPENAI_API_KEY"))
	if got != sentinel {
		t.Fatalf("vaulted value = %q, want the original secret %q -- the vault, not just the tag, is the source of truth this invariant depends on", got, sentinel)
	}
}

func TestAcceptanceScrubInvariant(t *testing.T) {
	db, bus, turnID, sentinel, golden, vaultDir := acceptanceScrubSetup(t)

	t.Run("sqlite_storage_bytes", func(t *testing.T) {
		acceptanceScrubAssertSQLiteBytes(t, db, turnID, sentinel, golden.ExpectedOutput)
	})

	t.Run("sse_event_payload_bytes", func(t *testing.T) {
		acceptanceScrubAssertSSEPayload(t, bus, sentinel, golden.ExpectedOutput)
	})

	t.Run("vault_key_reference_placeholder_present_in_storage", func(t *testing.T) {
		acceptanceScrubAssertVaultTag(t, db, turnID, golden.ExpectedTag)
	})

	t.Run("vault_actually_holds_the_secret", func(t *testing.T) {
		acceptanceScrubAssertVaultHolds(t, vaultDir, sentinel)
	})

	t.Run("cascade_chat_replay_stdout", func(t *testing.T) {
		t.Skip("no `cascade chat replay` command exists anywhere in this tree " +
			"(grep -rli replay cmd/ plugins/ docs/cli-reference/ finds no chat-related file), " +
			"and `cascade chat` itself cannot complete a round trip today: docs/cli-reference/chat.md " +
			"documents that every invocation returns errReplyGenerationUnavailable after recording the " +
			"turn (no daemon component generates an assistant reply yet -- internal/plugins/cascadepa_wiring.go), " +
			"and its one stdout path (writeOneShotResult, plugins/cascade-pa/cmd/chat.go) prints the " +
			"assistant's reply content, never the raw or scrubbed stored turn. This sub-check is unprovable " +
			"against the real tree as literally worded; reported as an honest gap, not silently passed.")
	})
}

// containsSentinel is a tiny, deliberately literal substring check --
// wrapping strings.Contains so both call sites above name the exact
// failing condition ("carries the raw sentinel") in one place.
func containsSentinel(haystack, needle string) bool {
	return len(needle) > 0 && strings.Contains(haystack, needle)
}
