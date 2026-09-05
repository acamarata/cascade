package forget

// The wiring proof, in both directions. memory.forget is served by a
// handler that may or may not have been given this pipeline, and the
// difference between the two is exactly the difference between a
// tombstone and a retirement. Both directions are asserted here so the
// wiring cannot be removed silently.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

// TestHandlerWithThePipelineRunsTheWholeRetirement drives the real RPC
// entry point and proves the pipeline ran: the index is clean and the
// backup note was sent.
func TestHandlerWithThePipelineRunsTheWholeRetirement(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	h := memory.NewHandler(f.store, f.clock, memory.WithForgetPipeline(f.pipe))

	res := callForget(t, h, id)

	if !res.Forgotten || res.ID != id {
		t.Fatalf("result = %+v, want the record retired", res)
	}
	if !res.Index.Row || !res.Index.Vector {
		t.Fatalf("index trace = %+v, want the row and vector removed", res.Index)
	}
	if !res.EventEmitted || len(f.sink.events) != 1 {
		t.Fatalf("the backup note did not travel: %+v, %d events", res, len(f.sink.events))
	}
	if len(f.searchHits(t, "pelicans")) != 0 {
		t.Fatal("the record is still findable after a forget through the handler")
	}
}

// TestHandlerWithoutThePipelineSaysWhatItDidNotDo is the same call with
// the wiring removed. It must still tombstone, and it must NOT claim the
// pipeline's guarantees: the index is untouched and the result says so
// rather than leaving a caller to assume otherwise.
//
// This is the failure mode the test above would miss on its own. If the
// composition root ever drops memory.WithForgetPipeline, the daemon
// behaves exactly like this, and the two tests together make that
// difference visible instead of silent.
func TestHandlerWithoutThePipelineSaysWhatItDidNotDo(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	h := memory.NewHandler(f.store, f.clock)

	res := callForget(t, h, id)

	if !res.Forgotten {
		t.Fatalf("result = %+v, want the record tombstoned even with no pipeline", res)
	}
	if len(f.sink.events) != 0 {
		t.Fatal("an unwired handler emitted a backup note it has no sink for")
	}
	if len(f.searchHits(t, "pelicans")) != 1 {
		t.Fatal("an unwired handler scrubbed an index it was never given")
	}
	for _, place := range []string{"projection rows and postings", "vector index", "backup and sync note"} {
		tr := findTrace(t, res.Traces, place)
		if tr.Disposition != memory.ForgetNotConfigured {
			t.Errorf("%s: disposition %q, want %q", place, tr.Disposition, memory.ForgetNotConfigured)
		}
	}
	if tr := findTrace(t, res.Traces, "record file"); tr.Disposition != memory.ForgetRemoved {
		t.Errorf("record file: disposition %q, want %q", tr.Disposition, memory.ForgetRemoved)
	}
}

// TestForgetDryRunChangesNothing keeps the rehearsal honest: it is the
// only way to look before leaping at a verb that never prompts.
func TestForgetDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	h := memory.NewHandler(f.store, f.clock, memory.WithForgetPipeline(f.pipe))
	before := treeSnapshot(t, f.base)

	params, err := json.Marshal(memory.ForgetParams{ID: id, DryRun: true, Reason: "asked to"})
	if err != nil {
		t.Fatalf("encoding params: %v", err)
	}
	res, err := h.Forget(context.Background(), params)
	if err != nil {
		t.Fatalf("memory.forget dry run: %v", err)
	}
	got, ok := res.(memory.ForgetResult)
	if !ok || !got.DryRun || got.Forgotten {
		t.Fatalf("dry-run result = %+v, want a rehearsal that retired nothing", res)
	}
	if len(f.sink.events) != 0 {
		t.Fatal("a dry run told the backup lane a record was forgotten")
	}
	after := treeSnapshot(t, f.base)
	if len(after) != len(before) {
		t.Fatalf("a dry run changed the tree: %d files before, %d after", len(before), len(after))
	}
	for path, body := range before {
		if after[path] != body {
			t.Errorf("a dry run changed %s", path)
		}
	}
}

// callForget drives memory.forget through the handler and decodes it.
func callForget(t *testing.T, h *memory.Handler, id string) memory.ForgetResult {
	t.Helper()
	params, err := json.Marshal(memory.ForgetParams{ID: id, Reason: "asked to"})
	if err != nil {
		t.Fatalf("encoding params: %v", err)
	}
	res, err := h.Forget(context.Background(), params)
	if err != nil {
		t.Fatalf("memory.forget: %v", err)
	}
	got, ok := res.(memory.ForgetResult)
	if !ok {
		t.Fatalf("memory.forget returned %T, want ForgetResult", res)
	}
	return got
}

// findTrace returns the trace for one place, failing when it is missing.
func findTrace(t *testing.T, traces []memory.ForgetTrace, place string) memory.ForgetTrace {
	t.Helper()
	for _, tr := range traces {
		if tr.Place == place {
			return tr
		}
	}
	t.Fatalf("no trace for %q in %+v", place, traces)
	return memory.ForgetTrace{}
}
