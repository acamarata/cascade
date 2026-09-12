package jobs

// Purpose: read-side enumeration over the job and resource_lease tables:
//	ListJobs (scope_glob + state filter, cursor pagination) and ListLeases
//	(scope_glob filter, cursor pagination) plus LeaseID/ParseLeaseID, the
//	external identifier a caller needs for a lease that has no surrogate
//	column of its own.
//
// CONTRACT NOTE (files_scope, quoted in the journal): P1-E29-W6-S60-T1's
// files_scope does not list this package at all -- job.list/job.show/
// lease.list/lease.release cannot be implemented against internal/jobs'
// existing exported surface, because no listing capability exists
// anywhere in this package (Store exposes GetJob(id) and GetLease(repoID,
// scopeGlob), both single-row lookups keyed by an id the caller must
// already know). Rather than leave the RPC surface impossible to build,
// this file adds the minimum read-only, purely additive capability the
// ticket's own acceptance criteria require, as its OWN new file (touching
// no existing internal/jobs file, to stay clear of the local-model lane
// work active elsewhere in this package during the same window). See this
// ticket's journal for the full contradiction, quoted both ways.
//
// Inputs: an open *Store (already migrated) plus a JobFilter/LeaseFilter.
// Outputs: a page of Job/ResourceLease rows plus an opaque cursor (empty
//	string means no further page); typed A-T7 errors for a malformed
//	filter.
// Constraints: pagination is OFFSET-based over a stable ORDER BY id (jobs)
//	/ (repo_id, scope_glob) (leases) -- adequate for this ticket's CLI/RPC
//	surface at the read volumes a single daemon's job table holds; a
//	future high-volume revision may replace this with a keyset cursor
//	without changing the wire shape (the cursor stays opaque to callers).
//	scope_glob filtering uses doublestar.Match (already an internal/jobs
//	dependency, glob.go) against the caller's pattern -- no second glob
//	engine.
//
// SPORT: rpc/job.* methods (ADD; type=rpc-handler, package=internal/rpc)
// depends on this read surface (P1-E29-W6-S60-T1).

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultListLimit is applied when a caller's filter requests no limit
// (Limit <= 0) or a limit above maxListLimit; maxListLimit caps a single
// page so an unbounded list.* call can never scan the whole table into
// one response.
const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// JobFilter is job.list's filter: an empty ScopeGlob or State matches
// every row for that field. Cursor, when non-empty, must be a value
// this package itself returned from a prior call.
type JobFilter struct {
	ScopeGlob string
	State     JobState
	Limit     int
	Cursor    string
}

// LeaseFilter is lease.list's filter, following JobFilter's shape.
type LeaseFilter struct {
	ScopeGlob string
	Limit     int
	Cursor    string
}

// clampLimit normalizes a caller-supplied limit to (0, maxListLimit].
func clampLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

// decodeOffsetCursor parses an opaque offset cursor minted by this file.
// An empty cursor decodes as offset 0. A malformed cursor is a typed
// error -- never silently reset to page 1, which would let a client's
// stale/garbled cursor replay pages it already saw.
func decodeOffsetCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 {
		return 0, cascade.Newf(cascade.KindInvalidInput, "jobs: malformed cursor %q", cursor)
	}
	return n, nil
}

// ListJobs returns one page of jobs matching filter, ordered by id, plus
// the cursor for the next page (empty when this page was the last one).
func (s *Store) ListJobs(ctx context.Context, filter JobFilter) ([]Job, string, error) {
	offset, err := decodeOffsetCursor(filter.Cursor)
	if err != nil {
		return nil, "", err
	}
	if filter.State != "" && !filter.State.Valid() {
		return nil, "", cascade.Newf(cascade.KindInvalidInput, "jobs: unknown job state %q", string(filter.State))
	}
	limit := clampLimit(filter.Limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, state, created_at, updated_at, capabilities, mutable_scope, risk_class,
			min_task_class, node_requirements, timeout_seconds, cost_ceiling, priority,
			consecutive_failed_attempts, consequence_class, data_class
		 FROM `+tableJob+` ORDER BY id`)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "jobs: list jobs")
	}
	defer func() { _ = rows.Close() }()

	var matched []Job
	for rows.Next() {
		j, err := scanJobRow(rows)
		if err != nil {
			return nil, "", err
		}
		if filter.State != "" && j.State != filter.State {
			continue
		}
		if filter.ScopeGlob != "" {
			ok, err := doublestar.Match(filter.ScopeGlob, j.MutableScope)
			if err != nil {
				return nil, "", cascade.Wrapf(cascade.KindInvalidInput, err, "jobs: invalid scope_glob %q", filter.ScopeGlob)
			}
			if !ok {
				continue
			}
		}
		matched = append(matched, j)
	}
	if err := rows.Err(); err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "jobs: list jobs: iterate")
	}
	return paginateJobs(matched, offset, limit)
}

// paginateJobs applies the offset/limit window to an already-filtered,
// already-ordered slice and mints the next cursor.
func paginateJobs(matched []Job, offset, limit int) ([]Job, string, error) {
	if offset > len(matched) {
		return nil, "", nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	page := matched[offset:end]
	next := ""
	if end < len(matched) {
		next = strconv.Itoa(end)
	}
	return page, next, nil
}

// jobRowScanner is the *sql.Rows subset scanJobRow needs -- mirrors
// lease_query.go's leaseScanner pattern so a single-row *sql.Row could
// also satisfy it if a future caller needs one.
type jobRowScanner interface {
	Scan(dest ...any) error
}

// scanJobRow decodes one job row, mirroring GetJob's own field list and
// decode logic (store_job.go) so the two never drift apart in shape.
func scanJobRow(row jobRowScanner) (Job, error) {
	var j Job
	var state, capsRaw, consequence, data string
	if err := row.Scan(&j.ID, &state, &j.CreatedAt, &j.UpdatedAt, &capsRaw, &j.MutableScope,
		&j.RiskClass, &j.MinTaskClass, &j.NodeRequirements, &j.TimeoutSeconds, &j.CostCeiling,
		&j.Priority, &j.ConsecutiveFailedAttempts, &consequence, &data); err != nil {
		return Job{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan job row")
	}
	j.State = DecodeJobState(state)
	if capsRaw != "" {
		if err := json.Unmarshal([]byte(capsRaw), &j.Capabilities); err != nil {
			return Job{}, cascade.Wrap(cascade.KindInvalidInput, err, "jobs: decode capabilities")
		}
	}
	cc, err := DecodeConsequenceClass(consequence)
	if err != nil {
		return Job{}, err
	}
	j.ConsequenceClass = cc
	dc, err := DecodeDataClass(data)
	if err != nil {
		return Job{}, err
	}
	j.DataClass = dc
	return j, nil
}

// leaseID is the external identifier this ticket mints for a
// ResourceLease, which has no surrogate id column of its own (model.go:
// "(RepoID, ScopeGlob) is the natural key -- no surrogate id is needed or
// DECIDED"). internal/daemon/subsystems_scheduler_events.go already
// established this exact "repo_id:scope_glob" convention for its own
// external-facing lease_id field; this file reuses it rather than
// minting a second convention for the same identity.
func leaseID(repoID, scopeGlob string) string {
	return repoID + ":" + scopeGlob
}

// ParseLeaseID splits an external lease id back into (repoID, scopeGlob).
// A lease id with no ":" separator, or an empty repoID, is malformed.
func ParseLeaseID(id string) (repoID, scopeGlob string, err error) {
	repoID, scopeGlob, ok := strings.Cut(id, ":")
	if !ok || repoID == "" {
		return "", "", cascade.Newf(cascade.KindInvalidInput, "jobs: malformed lease id %q", id)
	}
	return repoID, scopeGlob, nil
}

// LeaseID returns l's external identifier (leaseID's exported form, for
// callers outside this package that already hold a ResourceLease).
func LeaseID(l ResourceLease) string {
	return leaseID(l.RepoID, l.ScopeGlob)
}

// ListLeases returns one page of leases matching filter, ordered by
// (repo_id, scope_glob), plus the next-page cursor.
func (s *Store) ListLeases(ctx context.Context, filter LeaseFilter) ([]ResourceLease, string, error) {
	offset, err := decodeOffsetCursor(filter.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := clampLimit(filter.Limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` ORDER BY repo_id, scope_glob`)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "jobs: list leases")
	}
	defer func() { _ = rows.Close() }()

	var matched []ResourceLease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, "", err
		}
		if filter.ScopeGlob != "" {
			ok, err := doublestar.Match(filter.ScopeGlob, l.ScopeGlob)
			if err != nil {
				return nil, "", cascade.Wrapf(cascade.KindInvalidInput, err, "jobs: invalid scope_glob %q", filter.ScopeGlob)
			}
			if !ok {
				continue
			}
		}
		matched = append(matched, l)
	}
	if err := rows.Err(); err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "jobs: list leases: iterate")
	}
	if offset > len(matched) {
		return nil, "", nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	page := matched[offset:end]
	next := ""
	if end < len(matched) {
		next = strconv.Itoa(end)
	}
	return page, next, nil
}

// GetLeaseByID resolves an external lease id (leaseID's convention) back
// to its stored row via the existing GetLease(repoID, scopeGlob) path.
func (s *Store) GetLeaseByID(ctx context.Context, id string) (ResourceLease, bool, error) {
	repoID, scopeGlob, err := ParseLeaseID(id)
	if err != nil {
		return ResourceLease{}, false, err
	}
	return s.GetLease(ctx, repoID, scopeGlob)
}
