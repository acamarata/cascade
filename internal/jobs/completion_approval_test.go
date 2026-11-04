package jobs

// Purpose: the jobs_completion_approval store round trip against a real
//
//	SQLite file under t.TempDir() (P1-PBD-09a acceptance [8]).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newCompletionApprovalStore(t *testing.T) (*Store, func() error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	db := openTestDB(t)
	if err := ApplyCompletionApprovalSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyCompletionApprovalSchema: %v", err)
	}
	return NewStore(db), db.Close
}

func TestCompletionApprovalStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, closeDB := newCompletionApprovalStore(t)
	want := CompletionApproval{JobID: "job-1", Action: "complete", Nonce: "n-1",
		PolicyVersion: CompletionApprovalPolicyVersion, CreatedAt: 1000}
	if err := s.PutCompletionApproval(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := s.CompletionApprovalFor(ctx, "job-1", "complete")
	if err != nil || !found {
		t.Fatalf("For: found=%v err=%v", found, err)
	}
	if got != want || got.Denied {
		t.Fatalf("For = %+v, want %+v (Denied false)", got, want)
	}

	second := want
	second.Nonce, second.CreatedAt = "n-2", 2000
	if err := s.PutCompletionApproval(ctx, second); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if got, _, _ = s.CompletionApprovalFor(ctx, "job-1", "complete"); got != want {
		t.Fatalf("second Put changed the row: %+v, want %+v", got, want)
	}

	checkCompletionApprovalDenyDelete(ctx, t, s)

	if err := ApplyCompletionApprovalSchema(ctx, s.db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second migration apply: %v", err)
	}
	if !tableColumns(t, s.db, tableCompletionApproval)["nonce"] {
		t.Fatalf("table missing after re-apply")
	}

	var ce *cascade.Error
	if !errors.As(ErrApprovalPending, &ce) || ce != ErrApprovalPending ||
		ce.Msg != "jobs: completion approval is pending" || ce.Kind != cascade.KindElevationRequired {
		t.Fatalf("ErrApprovalPending identity/message/kind: %#v", ErrApprovalPending)
	}

	if err := closeDB(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, found, err = s.CompletionApprovalFor(ctx, "job-1", "complete"); err == nil || found {
		t.Fatalf("closed DB: found=%v err=%v, want false and an error", found, err)
	}
}

// checkCompletionApprovalDenyDelete checks MarkCompletionApprovalDenied on a
// present and an absent row, then DeleteCompletionApproval.
func checkCompletionApprovalDenyDelete(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()
	if err := s.MarkCompletionApprovalDenied(ctx, "job-1", "complete"); err != nil {
		t.Fatalf("MarkDenied: %v", err)
	}
	if got, _, _ := s.CompletionApprovalFor(ctx, "job-1", "complete"); !got.Denied {
		t.Fatalf("MarkDenied did not set Denied: %+v", got)
	}
	err := s.MarkCompletionApprovalDenied(ctx, "job-1", "absent")
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindNotFound {
		t.Fatalf("MarkDenied absent: err=%v, want KindNotFound", err)
	}

	if err := s.DeleteCompletionApproval(ctx, "job-1", "complete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, err := s.CompletionApprovalFor(ctx, "job-1", "complete"); found || err != nil {
		t.Fatalf("after Delete: found=%v err=%v, want false, nil", found, err)
	}
}
