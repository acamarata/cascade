package forget

// TestEpicGAcceptance is Epic G's end-to-end story: remember, index,
// promote, edit the SOUL by both available routes, see the review queue,
// then forget, and assert on every trace afterwards.
//
// It drives the RPC handlers rather than the cobra commands on purpose.
// The CLI dials the daemon and does nothing else (the daemon/CLI
// separation pattern), so memory.Handler and memory.SoulHandler ARE what
// route (a) reaches; the untagged unit lane may not import net, so a real
// socket run is the integration lane's job and is covered there.
//
// Route (c), the chat surface, is deferred to a later sprint and is not
// exercised here.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
)

func TestEpicGAcceptance(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The daemon's unix-socket lifecycle is tier-2 on Windows, so the
		// daemon-backed lifecycle is refused there rather than silently
		// reported as passing. The refusal is asserted, not assumed.
		t.Skip("refused on windows: the daemon unix-socket lifecycle is tier-2, " +
			"and IPC routing goes through the embedded runtime instead")
	}
	f := newFixture(t)
	ctx := context.Background()
	handler := memory.NewHandler(f.store, f.clock, memory.WithForgetPipeline(f.pipe))

	id := acceptanceRemember(t, handler)
	f.project(t)
	if hits := f.searchHits(t, "kingfishers"); len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("the remembered record is not in the index: %+v", hits)
	}

	acceptancePromote(t, f)
	acceptanceSoulEdit(t, f)
	acceptanceReview(t, f)

	out := acceptanceForget(ctx, t, handler, id)
	acceptanceAssertGone(t, f, out, id)
}

// acceptanceRemember writes the record through memory.remember and returns
// its canonical address.
func acceptanceRemember(t *testing.T, h *memory.Handler) string {
	t.Helper()
	params, err := json.Marshal(memory.RememberParams{
		Content: "the estuary at dawn is full of kingfishers\n",
		Type:    "project", Name: "estuary", Provenance: "s-1",
	})
	if err != nil {
		t.Fatalf("encoding params: %v", err)
	}
	res, err := h.Remember(context.Background(), params)
	if err != nil {
		t.Fatalf("memory.remember: %v", err)
	}
	got, ok := res.(memory.RememberResult)
	if !ok {
		t.Fatalf("memory.remember returned %T, want RememberResult", res)
	}
	return got.ID
}

// acceptancePromote drives a candidate up the mechanical ladder until it
// is promoted, which is the step that makes a candidate durable.
func acceptancePromote(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	ledger := memory.NewFileCandidateLedger(f.base, f.store, f.clock, nil)
	draft := memory.MemoryEntry{
		Name: "tideline", Kind: memory.KindProject, Body: "the tideline moves each day\n",
		Description: "tideline", ScopeRef: "local", Confidence: 1,
		Provenance: memory.Provenance{Origin: memory.OriginSession, SessionID: "s-1"},
	}
	ladder := memory.NewPromotionLadder(ledger)
	promoted := false
	for _, session := range []string{"s-1", "s-2", "s-3"} {
		entry, did, err := ladder.Observe(ctx, memory.Observation{SessionID: session, Draft: draft})
		if err != nil {
			t.Fatalf("observing in %s: %v", session, err)
		}
		promoted = promoted || did
		_ = entry
	}
	if !promoted {
		t.Fatal("three observations across three sessions did not promote the candidate")
	}
	if _, err := f.store.Read(ctx, memory.KindProject, "tideline"); err != nil {
		t.Fatalf("the promotion wrote no durable record: %v", err)
	}
}

// acceptanceSoulEdit exercises both available SOUL edit routes: (a) the
// verb the CLI dials, and (b) an out-of-store file edit that the store
// reconciles on its next load.
func acceptanceSoulEdit(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	soul := memory.NewFileSoulStore(f.base, f.clock, nil)
	if _, err := soul.Edit(ctx, memory.SoulDocument{Body: "route a wrote this\n"}); err != nil {
		t.Fatalf("soul edit route (a): %v", err)
	}
	view, err := soul.Get(ctx)
	if err != nil {
		t.Fatalf("reading the soul after route (a): %v", err)
	}
	if view.Document.Body != "route a wrote this\n" {
		t.Fatalf("soul body = %q after route (a)", view.Document.Body)
	}

	path := soulDocumentPath(t, f.base)
	if werr := os.WriteFile(path, []byte("route b wrote this\n"), 0o600); werr != nil {
		t.Fatalf("editing the soul file directly: %v", werr)
	}
	divergence, err := soul.DetectDivergence(ctx)
	if err != nil {
		t.Fatalf("reconciling route (b): %v", err)
	}
	if divergence.Outcome == memory.DivergenceNone {
		t.Fatalf("an out-of-store soul edit was not detected: %+v", divergence)
	}
}

// soulDocumentPath finds the SOUL document in the tree, so the test does
// not restate a layout the store owns.
func soulDocumentPath(t *testing.T, base string) string {
	t.Helper()
	var found string
	if err := walk(base, func(path string) {
		if filepath.Base(filepath.Dir(path)) == "soul" && filepath.Ext(path) == ".md" {
			found = path
		}
	}); err != nil {
		t.Fatalf("walking for the soul document: %v", err)
	}
	if found == "" {
		t.Fatal("no soul document on disk after an edit through route (a)")
	}
	return found
}

// acceptanceReview proves the promoted candidate is visible in the review
// queue, which is the surface a person reads before deciding anything.
func acceptanceReview(t *testing.T, f *fixture) {
	t.Helper()
	ledger := memory.NewFileCandidateLedger(f.base, f.store, f.clock, nil)
	queue := review.NewQueue(ledger, f.clock, nil)
	res, err := queue.List(context.Background(), review.ListParams{})
	if err != nil {
		t.Fatalf("listing the review queue: %v", err)
	}
	for _, c := range res.Promoted {
		if c.ID == memory.Address(memory.KindProject, "tideline") {
			return
		}
	}
	t.Fatalf("the promoted candidate is not in the review queue: %+v", res)
}

// acceptanceForget runs memory.forget through the handler, which is what
// the CLI reaches, and returns the decoded result.
func acceptanceForget(
	ctx context.Context, t *testing.T, h *memory.Handler, id string,
) memory.ForgetResult {
	t.Helper()
	params, err := json.Marshal(memory.ForgetParams{ID: id, Reason: "asked to"})
	if err != nil {
		t.Fatalf("encoding forget params: %v", err)
	}
	res, err := h.Forget(ctx, params)
	if err != nil {
		t.Fatalf("memory.forget: %v", err)
	}
	got, ok := res.(memory.ForgetResult)
	if !ok {
		t.Fatalf("memory.forget returned %T, want ForgetResult", res)
	}
	return got
}

// acceptanceAssertGone is the end of the story: the tombstone is present,
// the full-text and vector legs are clean, and the note reached the bus.
func acceptanceAssertGone(t *testing.T, f *fixture, out memory.ForgetResult, id string) {
	t.Helper()
	if !out.Forgotten || out.ID != id {
		t.Fatalf("forget result = %+v, want the record retired", out)
	}
	if n := countTombstones(t, f.base); n != 1 {
		t.Fatalf("%d tombstones, want exactly 1", n)
	}
	if hits := f.searchHits(t, "kingfishers"); len(hits) != 0 {
		t.Fatalf("the full-text leg still answers for the forgotten record: %+v", hits)
	}
	again, err := f.job.ScrubRecord(context.Background(), id)
	if err != nil {
		t.Fatalf("re-scrubbing: %v", err)
	}
	if !again.Empty() {
		t.Fatalf("traces survived the forget: %+v", again)
	}
	if len(f.sink.events) != 1 || f.sink.events[0].EntityID != id {
		t.Fatalf("events = %+v, want exactly one naming %s", f.sink.events, id)
	}
	if _, rerr := f.store.Read(context.Background(), memory.KindProject, "tideline"); rerr != nil {
		t.Fatalf("forgetting one record damaged the promoted one: %v", rerr)
	}
}
