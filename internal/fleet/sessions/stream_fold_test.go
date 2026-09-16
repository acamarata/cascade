package sessions

import (
	"context"
	"strings"
	"testing"
)

// Purpose (this file): the SSE fold and its decoder, driven over a plain
//   reader. No socket and no net import: Art.7.2 keeps network calls out of
//   the default unit lane, and ReadRecords takes an io.Reader precisely so
//   the parsing is reachable without a dial. The dial itself lives at the
//   composition root (cmd/cascade).
// SPORT: internal/fleet/sessions tests (ADD) — P1-E17-W4-S37-T2.

// collect runs readRecords over raw and returns what it produced.
func collect(t *testing.T, raw string) []SessionRecord {
	t.Helper()
	out := make(chan SessionRecord, 16)
	go func() {
		defer close(out)
		ReadRecords(context.Background(), strings.NewReader(raw), out)
	}()
	var got []SessionRecord
	for rec := range out {
		got = append(got, rec)
	}
	return got
}

// TestTheFoldJoinsMultipleDataLines pins the SSE grammar this has to
// honour: several "data:" lines in one block are ONE event joined with
// newlines, not several events.
func TestTheFoldJoinsMultipleDataLines(t *testing.T) {
	got := collect(t, "data: {\"session_id\":\"s1\",\ndata: \"harness\":\"claude\"}\n\n")
	if len(got) != 1 {
		t.Fatalf("decoded %d records from one block, want 1", len(got))
	}
	if got[0].SessionID != "s1" || got[0].Harness != "claude" {
		t.Fatalf("record = %+v", got[0])
	}
}

// TestEachBlockIsOneRecord proves consecutive events are kept separate.
func TestEachBlockIsOneRecord(t *testing.T) {
	got := collect(t,
		`data: {"session_id":"s1","harness":"claude"}`+"\n\n"+
			`data: {"session_id":"s2","harness":"codex"}`+"\n\n")
	if len(got) != 2 {
		t.Fatalf("decoded %d records, want 2", len(got))
	}
	if got[0].SessionID != "s1" || got[1].SessionID != "s2" {
		t.Fatalf("records = %+v", got)
	}
}

// TestAnUndecodableBlockIsSkippedNotFatal is the tolerance rule. The far
// end of this socket can write anything, and a watcher that dies on one
// malformed block is worse than one that misses it — the next block is
// very likely fine.
func TestAnUndecodableBlockIsSkippedNotFatal(t *testing.T) {
	got := collect(t,
		`data: {not json`+"\n\n"+
			`data: {"session_id":"s2","harness":"claude"}`+"\n\n")
	if len(got) != 1 {
		t.Fatalf("decoded %d records, want the malformed block skipped and the next one kept", len(got))
	}
	if got[0].SessionID != "s2" {
		t.Fatalf("record = %+v", got[0])
	}
}

// TestCommentsAndHeartbeatsAreIgnored proves the non-data lines an SSE
// server sends to keep a connection alive do not become records.
func TestCommentsAndHeartbeatsAreIgnored(t *testing.T) {
	got := collect(t,
		": heartbeat\n\n"+
			"id: 42\n"+
			`data: {"session_id":"s1","harness":"claude"}`+"\n\n"+
			": heartbeat\n\n")
	if len(got) != 1 {
		t.Fatalf("decoded %d records, want only the real event", len(got))
	}
}

// TestAStreamThatEndsMidBlockYieldsNothing proves a truncated final block
// is dropped rather than decoded from a partial body.
func TestAStreamThatEndsMidBlockYieldsNothing(t *testing.T) {
	got := collect(t, `data: {"session_id":"s1"`)
	if len(got) != 0 {
		t.Fatalf("decoded %d records from a truncated block", len(got))
	}
}

// TestACancelledContextStopsTheFold proves the reader honours cancellation
// rather than draining whatever remains.
func TestACancelledContextStopsTheFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := make(chan SessionRecord, 4)
	ReadRecords(ctx, strings.NewReader(`data: {"session_id":"s1"}`+"\n\n"), out)
	close(out)

	if len(out) != 0 {
		t.Fatalf("a cancelled fold produced %d records", len(out))
	}
}

// TestDecodeSessionEventRejectsNonRecords covers the decoder's own
// refusal, which the fold relies on to skip rather than fail.
func TestDecodeSessionEventRejectsNonRecords(t *testing.T) {
	for _, raw := range []string{"", "not json", "[]", "{", `"a string"`} {
		if _, ok := DecodeSessionEvent(raw); ok {
			t.Errorf("DecodeSessionEvent(%q) reported success", raw)
		}
	}
	rec, ok := DecodeSessionEvent(`{"session_id":"s1","harness":"claude","pid":42,"state":"active"}`)
	if !ok {
		t.Fatal("a real record was refused")
	}
	if rec.SessionID != "s1" || rec.PID != 42 || rec.State != "active" {
		t.Fatalf("record = %+v", rec)
	}
}

// TestTopicMatchesTheServersNamespace is the drift guard: the client
// filters on the same namespace the server publishes to, and they are the
// same constant rather than two copies of one string.
func TestTopicMatchesTheServersNamespace(t *testing.T) {
	if Topic != changedNamespace {
		t.Fatalf("Topic = %q but the server publishes to %q", Topic, changedNamespace)
	}
}
