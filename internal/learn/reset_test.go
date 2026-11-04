package learn

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type resetRecorder struct {
	query string
	args  []any
	calls int
}

func (r *resetRecorder) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	r.calls++
	r.query = q
	r.args = args
	return resetResult(2), nil
}

type resetResult int64

func (r resetResult) LastInsertId() (int64, error) { return 0, nil }
func (r resetResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestObservationResetterParameterised(t *testing.T) {
	q := &resetRecorder{}
	r := &SQLiteObservationResetter{DB: q}
	scope := "repo:opaque-id"
	n, err := r.Reset(context.Background(), scope, "code", "tier-1", false)
	if err != nil || n != 2 || q.calls != 1 {
		t.Fatalf("reset=(%d,%v), calls=%d", n, err, q.calls)
	}
	if strings.Contains(q.query, scope) || strings.Contains(q.query, "tier-1") || strings.Count(q.query, "?") != len(q.args) || len(q.args) != 3 {
		t.Fatalf("query=%q args=%v", q.query, q.args)
	}
	for i, want := range []string{scope, "code", "tier-1"} {
		if q.args[i] != want {
			t.Fatalf("arg %d=%v", i, q.args[i])
		}
	}
}

func TestLearnRPC_Reset_Refusals(t *testing.T) {
	q := &resetRecorder{}
	r := &SQLiteObservationResetter{DB: q}
	for _, p := range []struct {
		scope, tc, tier string
		all             bool
	}{
		{}, {"invalid", "", "", false}, {"global", "", "invalid", false}, {"global", "chat", "", false},
	} {
		if _, err := r.Reset(context.Background(), p.scope, p.tc, p.tier, p.all); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	if q.calls != 0 {
		t.Fatal("invalid reset reached database")
	}
	var missing *SQLiteObservationResetter
	if _, err := missing.Reset(context.Background(), "", "", "", true); err == nil {
		t.Fatal("accepted missing database")
	}
}
