package jobs

// Purpose: query.go's ListJobs/ListLeases/GetLeaseByID over a real
//	modernc-sqlite Store (newTestStore, this package's own established
//	fixture).
// SPORT: rpc/job.* methods (ADD) depends on this read surface
// (P1-E29-W6-S60-T1).

import (
	"context"
	"testing"
)

func TestListJobs_FiltersByStateAndScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := baseJob("job-a")
	a.MutableScope = "repo:/a"
	a.State = JobStatePending
	b := baseJob("job-b")
	b.MutableScope = "repo:/b"
	b.State = JobStateFailed
	for _, j := range []Job{a, b} {
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatalf("PutJob(%s): %v", j.ID, err)
		}
	}

	got, cursor, err := s.ListJobs(ctx, JobFilter{State: JobStateFailed})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "job-b" {
		t.Fatalf("ListJobs(state=failed) = %+v", got)
	}
	if cursor != "" {
		t.Fatalf("expected no next cursor, got %q", cursor)
	}

	got, _, err = s.ListJobs(ctx, JobFilter{ScopeGlob: "repo:/a"})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "job-a" {
		t.Fatalf("ListJobs(scope=repo:/a) = %+v", got)
	}
}

func TestListJobs_Pagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"job-1", "job-2", "job-3"} {
		j := baseJob(id)
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatalf("PutJob(%s): %v", id, err)
		}
	}

	page1, cursor1, err := s.ListJobs(ctx, JobFilter{Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1 = %+v cursor=%q, want 2 rows and a cursor", page1, cursor1)
	}
	page2, cursor2, err := s.ListJobs(ctx, JobFilter{Limit: 2, Cursor: cursor1})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2 = %+v cursor=%q, want 1 row and no cursor", page2, cursor2)
	}
}

func TestListJobs_MalformedCursorRefused(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.ListJobs(context.Background(), JobFilter{Cursor: "not-a-number"}); err == nil {
		t.Fatal("expected a typed error for a malformed cursor")
	}
}

func TestListJobs_UnknownStateRefused(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.ListJobs(context.Background(), JobFilter{State: "bogus"}); err == nil {
		t.Fatal("expected a typed error for an unknown state")
	}
}

func TestListLeases_FiltersByScopeAndPaginates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, sc := range []string{"/a", "/b", "/c"} {
		l := ResourceLease{RepoID: "repo1", ScopeGlob: sc, Holder: "job-x", IssuedAt: 1,
			TTLSeconds: 60, Epoch: 1, State: LeaseHeld}
		if err := s.PutLease(ctx, l); err != nil {
			t.Fatalf("PutLease(%s): %v", sc, err)
		}
	}

	got, _, err := s.ListLeases(ctx, LeaseFilter{ScopeGlob: "/a"})
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(got) != 1 || got[0].ScopeGlob != "/a" {
		t.Fatalf("ListLeases(scope=/a) = %+v", got)
	}

	page, cursor, err := s.ListLeases(ctx, LeaseFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListLeases page1: %v", err)
	}
	if len(page) != 2 || cursor == "" {
		t.Fatalf("page1 = %+v cursor=%q", page, cursor)
	}
}

func TestGetLeaseByID_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	l := ResourceLease{RepoID: "repo1", ScopeGlob: "/a", Holder: "job-x", IssuedAt: 1,
		TTLSeconds: 60, Epoch: 1, State: LeaseHeld}
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	id := LeaseID(l)
	got, ok, err := s.GetLeaseByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetLeaseByID(%q) = %+v, %v, %v", id, got, ok, err)
	}
	if got.RepoID != "repo1" || got.ScopeGlob != "/a" {
		t.Fatalf("GetLeaseByID(%q) = %+v", id, got)
	}
}

func TestParseLeaseID_MalformedRefused(t *testing.T) {
	if _, _, err := ParseLeaseID("no-colon-here"); err == nil {
		t.Fatal("expected a typed error for a malformed lease id")
	}
	if _, _, err := ParseLeaseID(":scope-only"); err == nil {
		t.Fatal("expected a typed error for an empty repo id")
	}
}
