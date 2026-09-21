// Purpose: allocate the ci_run identity a local run records under
// (P1-E25-W5-S51-T5), through the store rather than from a clock.
//
// THE LOCAL ID NAMESPACE IS THE NEGATIVE INTEGERS. ci_run is keyed
// (run_id, repo_id) and its github-actions rows carry GitHub's own
// workflow-run ids, which are always positive. A local run therefore
// allocates in the negatives: -1, -2, -3, ... per repository. That makes
// the two producers' id spaces PROVABLY disjoint rather than
// probabilistically so, which a clock-derived id never is -- and it is
// documented here as the namespace, so `cascade ci status` and any later
// reader can tell a local id from a hosted one by sign alone, even before
// consulting ci_run_source. The same value is reused as the local run's
// single ci_job id, keeping that table's namespace disjoint from Actions'
// job ids for the same reason.
//
// WHY NOT A TIMESTAMP. The previous NewLocalRunID returned
// clock.Now().UnixNano(), which collides outright under an injected fixed
// clock (every test run in the same process gets one id) and, in
// production, for any two runs inside the same nanosecond. Allocation
// through the store cannot collide with itself: the reservation INSERT is
// the allocation, so a second caller that raced to the same number fails
// the primary key and retries.
//
// Inputs: a *sql.DB with the ci_results schema applied, a repo id, the
// repository name, and the already-read timestamp.
// Outputs: the reserved run id, or a typed error.
// Constraints: no bare time.Now (forbidigo) -- the caller passes the
// instant it read from its injected clock.
// SPORT: internal.ci.ReserveLocalRun/ADDED, internal.ci.LocalRepoID/ADDED
//
//	(P1-E25-W5-S51-T5).

package ci

import (
	"context"
	"database/sql"
	"hash/fnv"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// firstLocalRunID is the first id allocated for a repository with no local
// history. Subsequent runs count down from it.
const firstLocalRunID = -1

// localRunIDAttempts bounds the reservation retry. A retry happens only
// when another `cascade ci run` reserved the same number between this
// caller's read and its insert; a handful of attempts covers any realistic
// contention, and a bound is what keeps a genuinely broken store from
// spinning forever.
const localRunIDAttempts = 8

// ReserveLocalRun allocates and immediately RESERVES the next local run id
// for repoID by inserting its ci_run row. The row starts in the queued
// state with an empty conclusion; runner.go's writeResult upserts the real
// status over it when the run finishes, so a crashed run leaves a visible
// queued row rather than a hole in the sequence.
func ReserveLocalRun(ctx context.Context, db *sql.DB, repoID int64, repoName string, now time.Time) (int64, error) {
	if db == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "ci: ReserveLocalRun requires a non-nil db")
	}
	var lastErr error
	for attempt := 0; attempt < localRunIDAttempts; attempt++ {
		runID, err := nextLocalRunID(ctx, db, repoID)
		if err != nil {
			return 0, err
		}
		err = insertReservation(ctx, db, runID, repoID, repoName, now)
		if err == nil {
			return runID, nil
		}
		lastErr = err
	}
	return 0, cascade.Wrapf(cascade.KindConflict, lastErr,
		"ci: could not reserve a local run id for repo %d after %d attempts", repoID, localRunIDAttempts)
}

// nextLocalRunID reads the lowest local (negative) run id recorded for
// repoID and returns the next one below it.
func nextLocalRunID(ctx context.Context, db *sql.DB, repoID int64) (int64, error) {
	var lowest sql.NullInt64
	row := db.QueryRowContext(ctx,
		`SELECT MIN(run_id) FROM `+tableRun+` WHERE repo_id = ? AND run_id < 0`, repoID)
	if err := row.Scan(&lowest); err != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "ci: reading the local run-id sequence for repo %d", repoID)
	}
	if !lowest.Valid {
		return firstLocalRunID, nil
	}
	return lowest.Int64 - 1, nil
}

// insertReservation writes the placeholder ci_run row that makes the id
// taken. A plain INSERT, deliberately not an upsert: the primary-key
// failure IS the collision detection ReserveLocalRun retries on, and an
// upsert here would silently overwrite a concurrent run's row instead.
func insertReservation(ctx context.Context, db *sql.DB, runID, repoID int64, repoName string, now time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO `+tableRun+` (run_id, repo_id, name, head_branch, head_sha, status, conclusion, created_at, updated_at)
		VALUES (?, ?, ?, '', '', ?, '', ?, ?)`,
		runID, repoID, repoName, string(RunStatusQueued), millis(now), millis(now))
	if err != nil {
		return cascade.Wrapf(cascade.KindConflict, err, "ci: reserving local run %d/%d", runID, repoID)
	}
	return nil
}

// LocalRepoID derives a stable ci_run.repo_id for a local repo root: an
// FNV-1a hash of its absolute-path string.
//
// It is a per-CHECKOUT identity, not a per-repository one: two clones of
// the same repository at different paths hash differently and so keep
// separate local histories, and moving a checkout starts a new one. That
// is the honest limit of deriving an id from a path, and it is the right
// trade here -- the alternative, resolving the real GitHub numeric
// repository id, would need the network egress this daemonless command
// deliberately has none of.
func LocalRepoID(absRepoRoot string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(absRepoRoot))
	// #nosec G115 -- a hash's bit pattern reinterpreted as int64 is exactly
	// what this id needs (any 64-bit value, not a magnitude), never an
	// out-of-range numeric conversion.
	return int64(h.Sum64())
}
