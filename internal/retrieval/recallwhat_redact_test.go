package retrieval

// Purpose: the AKIA-shaped-leak proof per outbound envelope field
// (rework brief: "one AKIA-shaped leak test per envelope field — snippet,
// path, memory key, rendered block, Errors[domain]"). Every test
// here drives the REAL RecallWhatService.Query end to end over the real
// egress.Engine (testEgress), so the redaction proof is against the
// actual substitution pass, never a hand-rolled string check.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// akiaSecret is the credential-shaped literal secrets.DefaultRegistry's
// detector recognises (an AWS access-key-id shape), matching the value
// F/S-10.T4's own tests already prove the detector catches.
const akiaSecret = "AKIAABCDEFGHIJKLMNOP"

// mustMarshal fails the test rather than returning a marshal error a
// caller could silently ignore.
func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestAKIALeak_ConversationSnippet: a turn's segment content carries the
// secret; it must not survive into Results[].Snippet. The conversation row
// itself must survive (its thread is public and in scope), so the secret's
// absence is the egress substitution firing, not the row being dropped.
func TestAKIALeak_ConversationSnippet(t *testing.T) {
	files, conv, mem := baselineLegs()
	conv.segs["t1"] = []RecallWhatSegment{{Content: "my key is " + akiaSecret + " keep it secret"}}
	svc := newBaselineService(t, files, conv, mem)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if strings.Contains(mustMarshal(t, resp), akiaSecret) {
		t.Fatalf("conversation snippet leaked the secret: %s", mustMarshal(t, resp))
	}
	survived := false
	for _, r := range resp.Results {
		survived = survived || (r.Domain == DomainConversation && strings.Contains(r.Snippet, "keep it secret"))
	}
	if !survived {
		t.Fatalf("the conversation row was dropped; the redaction proof needs it to survive: %s", mustMarshal(t, resp))
	}
}

// TestAKIALeak_MemoryPath: a memory record's Name (which becomes its
// Path, "<kind>/<name>") carries the secret; it must not survive into
// Results[].Path or Citations[].Path.
func TestAKIALeak_MemoryPath(t *testing.T) {
	files, conv, mem := baselineLegs()
	mem.rows = []memory.IndexedRecord{
		{ID: "project/" + akiaSecret, Name: akiaSecret, Kind: memory.KindProject, Body: "note", ScopeRef: "proj1"},
	}
	svc := newBaselineService(t, files, conv, mem)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	raw := mustMarshal(t, resp)
	if strings.Contains(raw, akiaSecret) {
		t.Fatalf("memory path leaked the secret: %s", raw)
	}
}

// TestAKIALeak_MemoryKey: the same record's chunk id
// ("memory:project/<name>") is its own named leak surface, distinct from
// Path (fix item 5) -- must not survive into Results[].ID or
// Citations[].ChunkID.
func TestAKIALeak_MemoryKey(t *testing.T) {
	files, conv, mem := baselineLegs()
	mem.rows = []memory.IndexedRecord{
		{ID: "project/" + akiaSecret, Name: akiaSecret, Kind: memory.KindProject, Body: "note", ScopeRef: "proj1"},
	}
	svc := newBaselineService(t, files, conv, mem)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, r := range resp.Results {
		if r.Domain == DomainMemory && strings.Contains(r.ID, akiaSecret) {
			t.Fatalf("Results[].ID leaked the raw secret: %q", r.ID)
		}
	}
	for _, c := range resp.Citations {
		if strings.Contains(c.ChunkID, akiaSecret) {
			t.Fatalf("Citations[].ChunkID leaked the raw secret: %q", c.ChunkID)
		}
	}
}

// TestAKIALeak_RenderedBlock: the Markdown citation footnote block is
// derived from the same (already-substituted) citations, but is its own
// named field the rework brief lists separately -- assert it directly
// rather than trusting the citations assertion to cover it transitively.
func TestAKIALeak_RenderedBlock(t *testing.T) {
	files, conv, mem := baselineLegs()
	mem.rows = []memory.IndexedRecord{
		{ID: "project/" + akiaSecret, Name: akiaSecret, Kind: memory.KindProject, Body: "note", ScopeRef: "proj1"},
	}
	svc := newBaselineService(t, files, conv, mem)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if strings.Contains(resp.Rendered, akiaSecret) {
		t.Fatalf("Rendered leaked the secret: %q", resp.Rendered)
	}
}

// TestAKIALeak_DomainError: a domain leg's raw error carries the secret
// (e.g. an index driver echoing a malformed query back); it must not
// survive into Errors[domain].
func TestAKIALeak_DomainError(t *testing.T) {
	files, conv, mem := baselineLegs()
	files.err = cascade.Newf(cascade.KindUnavailable, "files index unreadable: last query touched %s", akiaSecret)
	svc := newBaselineService(t, files, conv, mem)
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("a domain-unavailable leg must not fail the whole query: %v", err)
	}
	if strings.Contains(resp.Errors[DomainFile], akiaSecret) {
		t.Fatalf("Errors[file] leaked the secret: %q", resp.Errors[DomainFile])
	}
	if resp.Errors[DomainFile] == "" {
		t.Fatal("Errors[file] was empty; the domain-unavailable signal itself was lost")
	}
}
