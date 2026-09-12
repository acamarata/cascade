package rpc

// Purpose: the R-21.148 outbox-recorded effect handlers job.cancel,
//	job.retry and lease.release, built against JobHandlerDeps' duck-typed
//	JobStore/LeaseStore/OutboxRecorder seams (see jobs.go's CONTRACT NOTE
//	for the import-cycle proof this design works around).
//
// CONTRACT NOTE (files_scope, quoted in the journal): the full_desc's
// "same store transaction" requirement does not hold against the real
// tree — *jobs.Store's exported mutators accept no external *sql.Tx (out
// of files_scope to change), so the composition root's OutboxRecorder
// implementation cannot enroll the outbox write and the store write in
// one transaction. This file sequences RecordIntent -> the store effect
// -> Confirm, and leans on each effect already being idempotent by
// construction, so a crash between any two steps never double-applies:
//   - job.cancel's already-terminal-or-cancelling pre-check runs before
//     CancelJob is ever called;
//   - job.retry's new job row uses a DETERMINISTIC id derived from the
//     idempotency key, so a replayed RetryJob call resolves to the SAME
//     row via the real Store's INSERT...ON CONFLICT DO UPDATE;
//   - lease.release's ReleaseLease implementation (LeaseManager.Release)
//     already treats "already released, or never existed" as a no-op.
//
// SPORT: rpc/job.* methods (ADD) (P1-E29-W6-S60-T1).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// effectPayloadHash derives R-21.148's stable payload hash component from
// method and rawParams.
func effectPayloadHash(method string, rawParams json.RawMessage) string {
	sum := sha256.Sum256(append([]byte(method+":"), rawParams...))
	return hex.EncodeToString(sum[:])
}

func handleJobCancel(deps JobHandlerDeps) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p idParams
		if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "rpc: job.cancel: id is required")
		}
		j, ok, err := deps.Jobs.GetJob(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, cascade.Newf(cascade.KindNotFound, "rpc: job.cancel: job %q not found", p.ID)
		}
		// The scheduler's own advanceCancel idempotent-cancel rule
		// (internal/jobs/scheduler.go): already terminal or already
		// cancelling is success, no-op — CancelJob is never called.
		if j.Terminal() || j.State == "cancelling" {
			return struct{}{}, nil
		}
		hash := effectPayloadHash("job.cancel", raw)
		key := deps.Outbox.DeriveKey(p.ID, 0, hash)
		now := deps.nowUnix()
		if err := deps.Outbox.RecordIntent(ctx, p.ID, 0, hash); err != nil {
			return nil, err
		}
		if _, err := deps.Jobs.CancelJob(ctx, p.ID, now); err != nil {
			return nil, err
		}
		if err := deps.Outbox.Confirm(ctx, key); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
}

func handleJobRetry(deps JobHandlerDeps) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p idParams
		if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "rpc: job.retry: id is required")
		}
		orig, ok, err := deps.Jobs.GetJob(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, cascade.Newf(cascade.KindNotFound, "rpc: job.retry: job %q not found", p.ID)
		}
		if orig.State == "cancelled" {
			return nil, ErrNotRetryable
		}
		if orig.State != "failed" && orig.State != "rejected" {
			return nil, cascade.Newf(cascade.KindConflict, "rpc: job.retry: job %q is %s, not retryable", p.ID, orig.State)
		}
		gen := orig.ConsecutiveFailedAttempts + 1
		hash := effectPayloadHash("job.retry", raw)
		key := deps.Outbox.DeriveKey(p.ID, gen, hash)
		now := deps.nowUnix()
		if err := deps.Outbox.RecordIntent(ctx, p.ID, gen, hash); err != nil {
			return nil, err
		}
		newID := deriveRetryJobID(key)
		newJob, err := deps.Jobs.RetryJob(ctx, orig, newID, gen, now)
		if err != nil {
			return nil, err
		}
		if err := deps.Outbox.Confirm(ctx, key); err != nil {
			return nil, err
		}
		return newJob, nil
	}
}

// deriveRetryJobID derives job.retry's new job id deterministically from
// the outbox idempotency key, so a replayed call (same key) writes the
// SAME row, never a second one.
func deriveRetryJobID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte("job-retry:" + idempotencyKey))
	return "job-retry-" + hex.EncodeToString(sum[:16])
}

// leaseReleaseParams is lease.release's request shape. Elevated attempts
// (releasing another job's lease) attach an elevatedEnvelope on top.
type leaseReleaseParams struct {
	ID    string `json:"id"`
	AsJob string `json:"as_job"`
}

func handleLeaseRelease(deps JobHandlerDeps) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var env elevatedEnvelope
		_ = json.Unmarshal(raw, &env)
		args := raw
		if env.Attestation != nil {
			args = env.Args
		}
		var p leaseReleaseParams
		if err := json.Unmarshal(args, &p); err != nil || p.ID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "rpc: lease.release: id is required")
		}
		lease, ok, err := deps.Leases.GetLease(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, cascade.Newf(cascade.KindNotFound, "rpc: lease.release: lease %q not found", p.ID)
		}
		if lease.State == "released" {
			return struct{}{}, nil // already released: idempotent no-op
		}
		if p.AsJob == "" || p.AsJob != lease.Holder {
			if err := requireLeaseReleaseElevation(deps, env, args); err != nil {
				return nil, err
			}
		}
		hash := effectPayloadHash("lease.release", raw)
		key := deps.Outbox.DeriveKey(lease.Holder, lease.Epoch, hash)
		if err := deps.Outbox.RecordIntent(ctx, lease.Holder, lease.Epoch, hash); err != nil {
			return nil, err
		}
		if err := deps.Leases.ReleaseLease(ctx, p.ID); err != nil {
			return nil, err
		}
		if err := deps.Outbox.Confirm(ctx, key); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
}

// requireLeaseReleaseElevation implements lease.release's own local
// elevation gate — see jobs.go's CONTRACT NOTE for why this does not go
// through the closed, spec-transcribed elevationTable.
func requireLeaseReleaseElevation(deps JobHandlerDeps, env elevatedEnvelope, args json.RawMessage) error {
	hash := hashParams(args)
	if env.Attestation == nil {
		nonce, err := deps.Ledger.Issue("lease.release", hash)
		if err != nil {
			return err
		}
		return &ErrorObject{
			Code:    codeElevationRequired,
			Message: "elevation required: lease.release",
			Data:    elevationRequiredData{Reason: "ELEVATION_REQUIRED", Nonce: nonce},
		}
	}
	return VerifyAttestation(*env.Attestation, deps.Trust, deps.Ledger, "lease.release", hash, deps.nowTime())
}
