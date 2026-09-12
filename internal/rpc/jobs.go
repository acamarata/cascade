package rpc

// Purpose: the read-side job.list/job.show/lease.list JSON-RPC handlers
//	(P1-E29-W6-S60-T1) plus the JobStore/LeaseStore/ControllerGuard/
//	OutboxRecorder seams RegisterJobHandlers and jobs_effects.go's
//	job.cancel/job.retry/lease.release handlers are built against.
//
// CONTRACT NOTE (files_scope, quoted in the journal): the full_desc
// assumes this file imports internal/jobs (*jobs.Store, jobs.Job,
// jobs.ResourceLease) and internal/nodes (nodes.Role,
// nodes.RequireController) directly. Both are IMPORT CYCLES against the
// real tree, proven by `go build`:
//   internal/jobs -> internal/conductor -> internal/fleet/sessions ->
//   internal/rpc (dag.go/pews_compiler_*.go/planner.go/template_kinds.go
//   import internal/conductor; spawn_hook.go imports internal/rpc), and
//   internal/nodes -> internal/rpc directly (enroll.go, heartbeat.go,
//   serve.go, drain.go — internal/nodes is documented in serve.go's own
//   header as "an internal/rpc SERVER package").
// Neither edge can be added to (files outside files_scope, and reversing
// either would itself be a NEW cycle). This file instead duck-types the
// three seams it needs — JobStore, LeaseStore, ControllerGuard,
// OutboxRecorder below — the exact technique elevation_attest.go's own
// TrustStore interface already uses in this same package ("This ticket
// depends only on the lookup shape, not on how S-07.T6 persists
// records"). The composition root (cmd/cascade, which imports both
// internal/jobs and internal/nodes freely, and internal/rpc under the
// cmd-rpc-server-boundary's named exemption) implements all four against
// the real *jobs.Store/*jobs.LeaseManager/nodes.Role.
//
// SPORT: rpc/job.* methods (ADD; type=rpc-handler, package=internal/rpc),
//	rpc/lease.* methods (ADD; type=rpc-handler, package=internal/rpc)
//	(P1-E29-W6-S60-T1).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNotRetryable is job.retry's typed refusal for a CANCELLED job.
var ErrNotRetryable = cascade.New(cascade.KindConflict, "jobs: job is CANCELLED and is not retryable")

// JobRecord is the RPC wire shape for one job, mirroring
// internal/jobs.Job's exported field set (see this file's CONTRACT NOTE
// for why internal/rpc cannot reference that type directly).
type JobRecord struct {
	ID                        string   `json:"id"`
	State                     string   `json:"state"`
	CreatedAt                 int64    `json:"created_at"`
	UpdatedAt                 int64    `json:"updated_at"`
	Capabilities              []string `json:"capabilities,omitempty"`
	MutableScope              string   `json:"mutable_scope"`
	RiskClass                 string   `json:"risk_class"`
	MinTaskClass              string   `json:"min_task_class"`
	NodeRequirements          string   `json:"node_requirements,omitempty"`
	TimeoutSeconds            int64    `json:"timeout_seconds"`
	CostCeiling               float64  `json:"cost_ceiling"`
	Priority                  int      `json:"priority"`
	ConsecutiveFailedAttempts int64    `json:"consecutive_failed_attempts"`
	ConsequenceClass          string   `json:"consequence_class"`
	DataClass                 string   `json:"data_class"`
}

// Terminal reports whether r's state is one of the closed set no
// transition leaves (mirrors internal/jobs.JobState.Terminal's four
// values, since this package cannot import that type).
func (r JobRecord) Terminal() bool {
	switch r.State {
	case "accepted", "rejected", "cancelled", "failed":
		return true
	}
	return false
}

// LeaseRecord is the RPC wire shape for one lease, keyed by the external
// "<repo_id>:<scope_glob>" id convention internal/jobs' query.go mints
// (ResourceLease has no surrogate id column).
type LeaseRecord struct {
	ID         string `json:"id"`
	RepoID     string `json:"repo_id"`
	ScopeGlob  string `json:"scope_glob"`
	Holder     string `json:"holder"`
	IssuedAt   int64  `json:"issued_at"`
	TTLSeconds int64  `json:"ttl_seconds"`
	RenewCount int64  `json:"renew_count"`
	Epoch      int64  `json:"epoch"`
	State      string `json:"state"`
}

// JobListQuery is job.list's filter.
type JobListQuery struct {
	ScopeGlob string
	State     string
	Limit     int
	Cursor    string
}

// JobPage is job.list's page.
type JobPage struct {
	Jobs   []JobRecord `json:"jobs"`
	Cursor string      `json:"cursor,omitempty"`
}

// LeaseListQuery is lease.list's filter.
type LeaseListQuery struct {
	ScopeGlob string
	Limit     int
	Cursor    string
}

// LeasePage is lease.list's page.
type LeasePage struct {
	Leases []LeaseRecord `json:"leases"`
	Cursor string        `json:"cursor,omitempty"`
}

// JobStore is the job.* read/write seam. The composition root implements
// it against *internal/jobs.Store.
type JobStore interface {
	ListJobs(ctx context.Context, q JobListQuery) (JobPage, error)
	GetJob(ctx context.Context, id string) (JobRecord, bool, error)
	// CancelJob unconditionally applies the cancelling transition -- the
	// caller (handleJobCancel) is responsible for the idempotent
	// already-terminal-or-cancelling pre-check, so CancelJob is only ever
	// invoked when a real transition is needed.
	CancelJob(ctx context.Context, id string, now int64) (JobRecord, error)
	// RetryJob writes a new job row at newID, copying orig's DECIDED
	// field set with State=pending and ConsecutiveFailedAttempts=gen.
	// newID is caller-derived (deterministic from the outbox idempotency
	// key), so a replayed call resolves to the SAME row.
	RetryJob(ctx context.Context, orig JobRecord, newID string, gen int64, now int64) (JobRecord, error)
}

// LeaseStore is the lease.* read/write seam.
type LeaseStore interface {
	ListLeases(ctx context.Context, q LeaseListQuery) (LeasePage, error)
	GetLease(ctx context.Context, id string) (LeaseRecord, bool, error)
	ReleaseLease(ctx context.Context, id string) error
}

// ControllerGuard reports whether method is refused on this daemon
// (R-21.169: a node daemon refuses job.advance/lease.acquire/
// lease.release). The composition root implements it against
// nodes.RequireController(role, method) with role resolved once at
// startup.
type ControllerGuard interface {
	RequireController(method string) error
}

// OutboxRecorder is R-21.148's record/confirm seam. DeriveKey mirrors
// internal/jobs.DeriveIdempotencyKey's stable, derived shape without this
// package importing internal/jobs' OutboxSite vocabulary.
type OutboxRecorder interface {
	DeriveKey(jobID string, attemptGeneration int64, payloadHash string) string
	RecordIntent(ctx context.Context, jobID string, attemptGeneration int64, payloadHash string) error
	Confirm(ctx context.Context, key string) error
}

// JobHandlerDeps carries every dependency the six job.*/lease.* handlers
// need.
type JobHandlerDeps struct {
	Jobs   JobStore
	Leases LeaseStore
	Guard  ControllerGuard
	Outbox OutboxRecorder
	Ledger *NonceLedger
	Trust  TrustStore
	Clock  runtime.Clock
}

// RegisterJobHandlers registers job.list, job.show, job.cancel,
// job.retry, lease.list and lease.release against registry. job.cancel
// and lease.release run deps.Guard.RequireController first — the
// production caller internal/nodes.GuardedMethods' testonly-allow entry
// named as missing (the composition root's ControllerGuard
// implementation is what actually enumerates that slice; see
// cmd/cascade's adapter).
func RegisterJobHandlers(registry *Registry, deps JobHandlerDeps) {
	registry.Register("job.list", handleJobList(deps))
	registry.Register("job.show", handleJobShow(deps))
	registry.Register("job.cancel", guardedByRole(deps.Guard, "job.advance", handleJobCancel(deps)))
	registry.Register("job.retry", handleJobRetry(deps))
	registry.Register("lease.list", handleLeaseList(deps))
	registry.Register("lease.release", guardedByRole(deps.Guard, "lease.release", handleLeaseRelease(deps)))
}

// guardedByRole wraps next with guard.RequireController(method), refusing
// before next ever runs.
func guardedByRole(guard ControllerGuard, method string, next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if guard != nil {
			if err := guard.RequireController(method); err != nil {
				return nil, err
			}
		}
		return next(ctx, params)
	}
}

func handleJobList(deps JobHandlerDeps) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p JobListQuery
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "rpc: job.list: decode params")
			}
		}
		return deps.Jobs.ListJobs(ctx, p)
	}
}

type idParams struct {
	ID string `json:"id"`
}

func handleJobShow(deps JobHandlerDeps) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p idParams
		if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "rpc: job.show: id is required")
		}
		j, ok, err := deps.Jobs.GetJob(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, cascade.Newf(cascade.KindNotFound, "rpc: job.show: job %q not found", p.ID)
		}
		return j, nil
	}
}

func handleLeaseList(deps JobHandlerDeps) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p LeaseListQuery
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "rpc: lease.list: decode params")
			}
		}
		return deps.Leases.ListLeases(ctx, p)
	}
}

// nowUnix reads deps.Clock, never time.Now (Art.7.3).
func (deps JobHandlerDeps) nowUnix() int64 { return deps.Clock.Now().Unix() }

// nowTime reads deps.Clock as a time.Time, for VerifyAttestation's expiry
// check.
func (deps JobHandlerDeps) nowTime() time.Time { return deps.Clock.Now() }
