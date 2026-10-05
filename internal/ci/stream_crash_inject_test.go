// Purpose: the parent half of the SEC-33 crash recipe (the child and the kill
// sites are in stream_crash_test.go): for each of the eight injected kill
// points, assert the on-disk state the kill left, run Dispatcher.Resume over
// the SAME databases and assert exactly one ci_run (and one ci_job) per
// (attempt, kind), every outbox row confirmed, the attempt terminal, and
// that a second Resume re-dispatches nothing.
//
// SPORT: internal.ci.Dispatcher.Resume/TESTED (P1-CI-01).
package ci

import (
	"context"
	"database/sql"
	goruntime "runtime"
	"testing"
)

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// crashCase is one kill point and the on-disk state it must have left.
type crashCase struct {
	site, phase string
	left        func(t *testing.T, ci, jb *sql.DB) bool
}

func crashCases() []crashCase {
	jobsCount := func(where string) func(t *testing.T, ci, jb *sql.DB) bool {
		return func(t *testing.T, _, jb *sql.DB) bool {
			return countRows(t, jb, `SELECT COUNT(*) FROM jobs_outbox WHERE `+where) >= 1
		}
	}
	ciCount := func(query string) func(t *testing.T, ci, jb *sql.DB) bool {
		return func(t *testing.T, ci, _ *sql.DB) bool { return countRows(t, ci, query) >= 1 }
	}
	return []crashCase{
		{"intent", "before", func(t *testing.T, _, jb *sql.DB) bool {
			return countRows(t, jb, `SELECT COUNT(*) FROM jobs_outbox`) == 0
		}},
		{"intent", "after", jobsCount(`state = 'intent'`)},
		{"subjob", "before", func(t *testing.T, ci, jb *sql.DB) bool {
			return countRows(t, ci, `SELECT COUNT(*) FROM ci_run`) == 0 && countRows(t, jb, `SELECT COUNT(*) FROM jobs_outbox WHERE state = 'intent'`) == 2
		}},
		{"subjob", "after", ciCount(`SELECT COUNT(*) FROM ci_run WHERE status = 'queued'`)},
		{"job", "before", func(t *testing.T, ci, _ *sql.DB) bool {
			// The result is written in one transaction, so a kill before the
			// ci_job insert leaves the reserved ci_run still queued.
			return countRows(t, ci, `SELECT COUNT(*) FROM ci_run WHERE status = 'queued'`) >= 1 && countRows(t, ci, `SELECT COUNT(*) FROM ci_job`) == 0
		}},
		{"job", "after", ciCount(`SELECT COUNT(*) FROM ci_job`)},
		{"confirm", "before", jobsCount(`state = 'effect'`)},
		{"confirm", "after", jobsCount(`state = 'confirmed'`)},
	}
}

// assertResumedExactlyOnce checks the end state after Resume.
func assertResumedExactlyOnce(t *testing.T, ciDB, jobsDB *sql.DB) {
	t.Helper()
	for what, c := range map[string]struct {
		db    *sql.DB
		query string
		want  int
	}{
		"distinct sub-jobs (format, lint)":     {ciDB, `SELECT COUNT(DISTINCT name) FROM ci_run WHERE name LIKE 'stream-%'`, 2},
		"sub-jobs with more than one ci_run":   {ciDB, `SELECT COUNT(*) FROM (SELECT name FROM ci_run GROUP BY name HAVING COUNT(*) > 1)`, 0},
		"completed ci_run rows (one per kind)": {ciDB, `SELECT COUNT(*) FROM ci_run WHERE status = 'completed'`, 2},
		"completed ci_job rows":                {ciDB, `SELECT COUNT(*) FROM ci_job WHERE status = 'completed'`, 2},
		"ci_step rows (one per kind)":          {ciDB, `SELECT COUNT(*) FROM ci_step`, 2},
		"completed jobs without a step row":    {ciDB, `SELECT COUNT(*) FROM ci_job j WHERE NOT EXISTS (SELECT 1 FROM ci_step s WHERE s.job_id = j.job_id)`, 0},
		"outbox rows not confirmed":            {jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE state != 'confirmed'`, 0},
		"outbox rows (one per kind)":           {jobsDB, `SELECT COUNT(*) FROM jobs_outbox`, 2},
		"terminal attempts":                    {ciDB, `SELECT COUNT(*) FROM ci_stream_attempt WHERE state = 'terminal'`, 1},
	} {
		if got := countRows(t, c.db, c.query); got != c.want {
			t.Fatalf("%s = %d, want %d", what, got, c.want)
		}
	}
}

func TestStreamCrashInjection(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the kill-site child needs POSIX process semantics for the shell stand-ins")
	}
	for _, tc := range crashCases() {
		t.Run(tc.phase+"-"+tc.site, func(t *testing.T) {
			f := newCrashFixture(t)
			runCrashChild(t, f, tc.site+":"+tc.phase)

			d, fx, ciDB, jobsDB := f.dispatcher(t, "sqlite", nil)
			defer func() { _ = ciDB.Close(); _ = jobsDB.Close() }()
			if !tc.left(t, ciDB, jobsDB) {
				t.Fatalf("the kill %s %s did not leave the expected on-disk state (it was not injected at the site)", tc.phase, tc.site)
			}
			if _, err := d.Resume(context.Background()); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			assertResumedExactlyOnce(t, ciDB, jobsDB)

			ran := len(fx.commands())
			again, err := d.Resume(context.Background())
			if err != nil || again != (ResumeReport{}) || len(fx.commands()) != ran {
				t.Fatalf("second Resume = %+v, %v, commands %d->%d; a terminal sub-job must never be re-dispatched", again, err, ran, len(fx.commands()))
			}
			if ran == 0 && tc.site != "confirm" && tc.site != "job" {
				t.Fatalf("Resume ran no command although the kill left work to do (%s %s)", tc.phase, tc.site)
			}
		})
	}
}
