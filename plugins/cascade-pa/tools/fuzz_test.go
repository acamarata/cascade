package tools

// Purpose (this file): FuzzCpaToolInput — the added-decoder fuzz target
//   06 §5 rule 7 requires, because these three tools decode arbitrary
//   harness input.
//
// IT DRIVES BOTH SIDES. A target that only fed the fuzzer's bytes into
//   the PARAMETERS would never reach the code that cuts an excerpt out of
//   stored content, which is where this package's one real defect lived
//   (a byte-sliced excerpt cutting a multi-byte rune in half). So the
//   fake service below answers with content derived from the same input:
//   the fuzzer drives the query AND the corpus it searches.
//
// Corpus: plugins/cascade-pa/tools/testdata/fuzz/FuzzCpaToolInput/
//   (R-21.266 — package-local, and a fuzz check names exactly one
//   package, never a /... pattern).
//
// SPORT: plugins/cascade-pa:mcp-tools (TEST) — P1-E20-W5-S43-T4.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// fuzzConversations answers every read with content built from the same
// bytes the fuzzer handed the decoder.
type fuzzConversations struct{ content string }

func (f *fuzzConversations) Append(_ context.Context, _ AppendRequest) (AppendResult, error) {
	return AppendResult{ThreadID: "t1", TurnID: "u1", CreatedAt: "2026-09-20T00:00:00Z"}, nil
}

func (f *fuzzConversations) Thread(_ context.Context, id string) (ThreadDetail, error) {
	return ThreadDetail{ThreadID: id, Turns: []TurnRecord{
		{TurnID: "u1", Role: "user", Content: f.content, Seq: 1},
		{TurnID: "u2", Role: "assistant", Content: "x" + f.content, Seq: 2},
	}}, nil
}

func (f *fuzzConversations) Search(_ context.Context, _ SearchRequest) ([]SearchResult, error) {
	// The index answers with the same bytes the decoder was handed, so the
	// fuzzer drives the excerpt cutter through the indexed path too.
	return []SearchResult{{ThreadID: "t1", TurnID: "u1", Content: f.content, Score: 1}}, nil
}

func (f *fuzzConversations) Threads(_ context.Context) ([]ThreadSummary, error) {
	return []ThreadSummary{{ID: "t1", Title: f.content, UpdatedAt: "2026-09-20T00:00:00Z"}}, nil
}

// cpaFuzzSeeds are the in-code boundary cases, alongside the committed
// corpus files Go loads from testdata/fuzz/FuzzCpaToolInput/.
var cpaFuzzSeeds = []string{
	``,
	`null`,
	`{}`,
	`{"content":"hello"}`,
	`{"content":"hello","thread_id":"t1","sensitivity":"public"}`,
	`{"content":"hello","sensitivity":"NOT-A-TIER"}`,
	`{"thread_id":"t1","limit":20,"before_turn_id":"u1"}`,
	`{"limit":-1}`,
	`{"limit":2147483647}`,
	`{"query":"needle","limit":10}`,
	`{"query":"","thread_id":"t1"}`,
	`{"query":"日本"}`,
	`{"content":"` + strings.Repeat("日", 40) + `x"}`,
	`{"unknown_field":1}`,
	`{"content":`,
	`[]`,
	`"just a string"`,
	`0`,
}

func FuzzCpaToolInput(f *testing.F) {
	for _, s := range cpaFuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		d := NewDispatcher(&fuzzConversations{content: raw})
		for _, name := range Names() {
			out, err := d.Dispatch(context.Background(), name, []byte(raw))
			if err != nil {
				// A refusal is a perfectly good answer to arbitrary
				// bytes. What must never happen is a panic, which the
				// fuzzer catches on its own.
				continue
			}
			assertToolResultIsWellFormed(t, name, raw, out)
		}
	})
}

// assertToolResultIsWellFormed is the property every successful tool
// result must hold, whatever the input was.
func assertToolResultIsWellFormed(t *testing.T, tool, raw string, out []byte) {
	t.Helper()
	if !json.Valid(out) {
		t.Fatalf("%s(%q) returned invalid JSON: %q", tool, raw, out)
	}
	if !utf8.Valid(out) {
		t.Fatalf("%s(%q) returned invalid UTF-8: %q", tool, raw, out)
	}
	if tool != ToolSearch {
		return
	}
	// The excerpt is the one place this package cuts a string at a byte
	// offset, so it is the one place a rune can be halved. A U+FFFD here
	// is a replacement character standing in the operator's own words.
	var res SearchOutput
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%s(%q): decoding own result %q: %v", tool, raw, out, err)
	}
	for _, hit := range res.Results {
		if !utf8.ValidString(hit.Excerpt) {
			t.Fatalf("%s(%q): excerpt is not valid UTF-8: %q", tool, raw, hit.Excerpt)
		}
		// Only flag a replacement character the EXCERPT introduced: an
		// input that genuinely contained U+FFFD must be allowed to come
		// back out again.
		if strings.ContainsRune(hit.Excerpt, utf8.RuneError) && !strings.ContainsRune(raw, utf8.RuneError) {
			t.Fatalf("%s(%q): excerpt introduced U+FFFD, so a rune was cut: %q", tool, raw, hit.Excerpt)
		}
	}
}
