package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The search side of the projection, and the vector leg that fills the
// other half of a recall: what a query returns, what it must never
// return, and that the order it returns them in does not vary. The
// fixture, the real SQLite database and the test-only embedder all live in
// db_projection_test.go; this file is split from it only to keep every
// file inside the 300-line cap.
func TestSearch_TermInBodyReturnsRecord(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	writeEntry(t, f, "beta", "the beta body mentions cormorants\n", "second")
	mustRun(t, f)

	hits, err := f.job.Search(context.Background(), "pelicans", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "project/alpha" {
		t.Fatalf("Search(pelicans) = %+v, want just project/alpha", hits)
	}
	if hits[0].Description != "first" {
		t.Fatalf("hit lost its description: %+v", hits[0])
	}
}

func TestSearch_QueryShapes(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "shared word pelicans\n", "first")
	writeEntry(t, f, "beta", "shared word cormorants\n", "second")
	mustRun(t, f)
	ctx := context.Background()

	cases := []struct {
		name  string
		query string
		limit int
		want  int
	}{
		{"term in both records", "shared", 0, 2},
		{"conjunction narrows", "shared pelicans", 0, 1},
		{"conjunction with an absent term", "shared penguins", 0, 0},
		{"unknown term", "penguins", 0, 0},
		{"empty query matches nothing", "", 0, 0},
		{"punctuation only matches nothing", "---", 0, 0},
		{"limit caps the result", "shared", 1, 1},
		{"term from the description", "second", 0, 1},
		{"term from the name", "alpha", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := f.job.Search(ctx, tc.query, tc.limit)
			if err != nil {
				t.Fatalf("Search(%q): %v", tc.query, err)
			}
			if len(hits) != tc.want {
				t.Fatalf("Search(%q) returned %d hits, want %d", tc.query, len(hits), tc.want)
			}
		})
	}
}

func TestSearch_ExpiredRecordIsNotReturned(t *testing.T) {
	f := newProjection(t)
	e := validEntry()
	e.Name, e.Body, e.Description = "alpha", "body pelicans\n", "first"
	e.ExpiresAt = ptrTime(fixedNow.Add(time.Hour))
	if err := f.files.Write(context.Background(), e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	mustRun(t, f)
	assertHits(t, f, "pelicans", "project/alpha")

	f.clock.advance(2 * time.Hour)
	assertHits(t, f, "pelicans")
}

func TestSearch_CorruptRowRefusesRatherThanShortening(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)

	ctx := context.Background()
	if err := f.kv.Put(ctx, projectionNamespace, recordKey("project/alpha"), []byte("{corrupt")); err != nil {
		t.Fatalf("corrupting the row: %v", err)
	}
	if _, err := f.job.Search(ctx, "pelicans", 0); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Search over a corrupt row returned %v, want an integrity refusal", err)
	}
}

func TestRun_AcrossEveryKind(t *testing.T) {
	f := newProjection(t)
	ctx := context.Background()
	for i, kind := range AllKinds() {
		e := validEntry()
		e.Kind, e.Name = kind, "record"
		e.Body = "shared term unique" + strings.Repeat("x", i) + "\n"
		if err := f.files.Write(ctx, e); err != nil {
			t.Fatalf("writing %s: %v", kind, err)
		}
	}
	res := mustRun(t, f)
	if res.Scanned != len(AllKinds()) || res.Upserted != len(AllKinds()) {
		t.Fatalf("run = %+v, want one record per kind", res)
	}
	hits, err := f.job.Search(ctx, "shared", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != len(AllKinds()) {
		t.Fatalf("Search returned %d hits, want one per kind", len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].ID >= hits[i].ID {
			t.Fatalf("results are not ordered by id: %s then %s", hits[i-1].ID, hits[i].ID)
		}
	}
}

func TestRun_WithoutAVectorLegStillProjects(t *testing.T) {
	files, _, _ := newStore(t)
	db := openTestDB(t)
	job := NewProjectionJob(files, db, nil, nil, files.clock)
	e := validEntry()
	e.Body = "body pelicans\n"
	if err := files.Write(context.Background(), e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	res, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Upserted != 1 || res.Embedded != 0 {
		t.Fatalf("run with no vector leg = %+v, want 1 upserted and 0 embedded", res)
	}
	hits, err := job.Search(context.Background(), "pelicans", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("Search = %+v, %v, want one hit", hits, err)
	}
}

func TestRun_EmbedderFailureIsReportedAndTheRowStillLands(t *testing.T) {
	f := newProjection(t)
	f.embedder.failOn = "pelicans"
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	writeEntry(t, f, "beta", "body cormorants\n", "second")

	res := mustRun(t, f)
	if res.Upserted != 2 || res.Embedded != 1 || res.Failed != 1 {
		t.Fatalf("run with a failing embedder = %+v, want 2 upserted, 1 embedded, 1 failed", res)
	}
	if res.Failures[0].ID != "project/alpha" {
		t.Fatalf("failure names %q, want project/alpha", res.Failures[0].ID)
	}
	assertHits(t, f, "pelicans", "project/alpha")
}

func TestRun_EmbedderContractViolationIsRefused(t *testing.T) {
	f := newProjection(t)
	f.embedder.wrongWidth = true
	writeEntry(t, f, "alpha", "body pelicans\n", "first")

	res := mustRun(t, f)
	if res.Failed != 1 || !strings.Contains(res.Failures[0].Reason, "does not match model") {
		t.Fatalf("run with a contract-violating embedder = %+v, want a batch refusal", res)
	}
	if n := mustVectorCount(t, f); n != 0 {
		t.Fatalf("a refused batch still wrote %d vectors", n)
	}
}
