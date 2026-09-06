package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests for Manager.Verify — the
// missing/orphaned/vector-incomplete checks and the R-21.189 generation
// marker's drift status, including the fail-closed absent/unparseable
// cases.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRecallIndexVerifyCleanAfterRebuild proves a fresh rebuild verifies
// clean: no missing, no orphaned, no vector-incomplete, marker current.
func TestRecallIndexVerifyCleanAfterRebuild(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody text"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Clean() {
		t.Fatalf("want a clean report, got %+v", report)
	}
}

// TestRecallIndexVerifyDetectsOrphaned proves an FTS5 document with no
// catalog record is reported orphaned.
func TestRecallIndexVerifyDetectsOrphaned(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody text"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	orphan := retrieval.Chunk{ID: retrieval.ChunkID([]byte("orphan content")), Path: "orphan.md",
		Content: []byte("orphan content"), Lang: "markdown"}
	if err := h.index.Write(ctx, testCorpus.ID, []retrieval.Chunk{orphan}); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.Orphaned) != 1 || report.Orphaned[0] != orphan.ID {
		t.Fatalf("want the orphaned chunk reported, got %+v", report.Orphaned)
	}
}

// TestRecallIndexVerifyNoIndexIsNotFound proves Verify refuses with
// KindNotFound before any rebuild has ever run.
func TestRecallIndexVerifyNoIndexIsNotFound(t *testing.T) {
	h := newHarness(t)
	m := h.manager(nil)
	if _, err := m.Verify(context.Background()); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("want KindNotFound, got %v", err)
	}
}

// TestRecallIndexGenerationDrift is the ticket's contract-named smoke
// test for the marker-drift status: a mismatched marker reads as
// DRIFTED. The fail-closed absent/unparseable cases are the
// TestRecallIndexGenerationDrift*-prefixed tests below; this is the
// literal name the ticket's own checks list runs.
func TestRecallIndexGenerationDrift(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	h.hash.hash = "changed-commit:digest"
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.MarkerStatus != lifecycle.MarkerDrifted {
		t.Fatalf("want MarkerDrifted after the tree hash changed, got %q", report.MarkerStatus)
	}
}

// TestRecallIndexGenerationDriftAbsentMarker proves an absent marker
// reads as DRIFTED, never as current (R-21.189 fail-closed rule).
func TestRecallIndexGenerationDriftAbsentMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// Simulate an absent marker by deleting the key this package wrote.
	if err := h.store.Delete(ctx, retrieval.IndexNamespace, "lifecycle:generation"); err != nil {
		t.Fatalf("delete marker: %v", err)
	}
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.MarkerStatus != lifecycle.MarkerDrifted {
		t.Fatalf("want an absent marker to read as drifted, got %q", report.MarkerStatus)
	}
}

// TestRecallIndexGenerationDriftUnparseableMarker proves an unparseable
// marker also reads as DRIFTED, never as current.
func TestRecallIndexGenerationDriftUnparseableMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := h.store.Put(ctx, retrieval.IndexNamespace, "lifecycle:generation", []byte("not json")); err != nil {
		t.Fatalf("corrupt marker: %v", err)
	}
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.MarkerStatus != lifecycle.MarkerDrifted {
		t.Fatalf("want an unparseable marker to read as drifted, got %q", report.MarkerStatus)
	}
}

// TestRecallIndexGenerationDriftMismatch proves a stored marker that no
// longer matches the current tree hash reads as DRIFTED.
func TestRecallIndexGenerationDriftMismatch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	h.hash.hash = "a-different-commit:digest"
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.MarkerStatus != lifecycle.MarkerDrifted {
		t.Fatalf("want a mismatched marker to read as drifted, got %+v", report)
	}
}
