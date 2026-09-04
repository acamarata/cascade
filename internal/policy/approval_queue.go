// Package policy (approval_queue.go): Purpose: the vocabulary and the
//
//	construction of the approval queue — the place a human says yes to an
//	ask-tier (L2/L3) action. This file declares the queue's types and its
//	constructor; approval_queue_enqueue.go admits actions to it and
//	approval_queue_decide.go decides and redeems them.
//
// Inputs: an ApprovalQueueConfig naming the B-layer store the single-use
//
//	ledger writes through, the capability registry and grant store the
//	queue re-consults at redemption time, an injected Clock, the R-14.29
//	batching numerics from autonomy_config.go, and the optional audit
//	recorder, deny-list and token minter.
//
// Outputs: *StoreApprovals, the ApprovalQueue implementation.
// Constraints: two properties govern every line of this subsystem.
//
//	FIRST, AN APPROVAL BINDS TO EXACTLY WHAT WAS SHOWN. The queue hashes
//	the action and its parameters itself rather than trusting a digest a
//	caller hands it, records the digests on the entry, and re-derives them
//	from the action presented at redemption. A request that changed between
//	display and execution therefore fails the comparison and is refused;
//	an approval is never a permission for a CATEGORY of action or for a
//	SESSION, only for the one action whose bytes were hashed.
//	SECOND, NOTHING OUTLIVES ITS WINDOW OR ITS SCOPE. exp is capped at
//	MaxApprovalTTL at mint time and is not configurable upward; expiry,
//	cancellation and the revocation of the standing grant the entry was
//	admitted under each invalidate a pending OR an already-granted
//	approval, which is why the queue keeps no grant cache and re-Checks the
//	grant store at redemption.
//	Everything else follows from those two: fail closed on an unknown id, a
//	replayed decision, a decision after expiry, a damaged ledger row and an
//	unreadable store; deterministic ordering by admission sequence with no
//	map iteration in anything a user sees or that decides an outcome; and
//	a GetPending payload of three fields, so no bridge can ever carry a
//	token, a nonce or an action hash.
//
// SPORT: internal/policy ApprovalQueue/ADDED, StoreApprovals/ADDED,
//
//	ApprovalToken/ADDED, PendingEntry/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// MaxApprovalTTL is 06-FORGE-SPEC §5.24's ceiling on a token's lifetime.
// It is a CEILING, not a default and not a configurable value: the batch
// window may be shorter, never longer, and a caller cannot raise it.
const MaxApprovalTTL = 5 * time.Minute

// approvalSweepInterval bounds the background sweep to at most one pass a
// minute. GetPending prunes unconditionally on every call, so the sweep is
// a floor on freshness for an idle queue, not the primary expiry path.
const approvalSweepInterval = time.Minute

// maxRetainedBatches bounds how many full batches may be pending at once.
// The pending ceiling is this times the configured per-batch cap, which
// keeps the ceiling tied to the operator's own sizing rather than to a
// second, unrelated number.
const maxRetainedBatches = 8

// maxApprovalTextLen bounds the caller-supplied action and summary
// strings. Both are shown to a human or hashed; an unbounded one is a
// display-injection and storage hazard rather than a legitimate action.
const maxApprovalTextLen = 512

// ApprovalQueue is the queue the policy engine's ask-verdict path feeds and
// the CLI/RPC approval surface (S-18.T6) drives.
type ApprovalQueue interface {
	// Enqueue admits one ask-tier action, coalescing it into the open
	// batch or returning the existing request id for a duplicate.
	Enqueue(ctx context.Context, req EnqueueRequest) (EnqueueResult, error)
	// GetPending returns every entry awaiting a decision, oldest first,
	// after pruning whatever expired.
	GetPending(ctx context.Context) ([]PendingEntry, error)
	// Decide records one human decision per element, in order. A refusal
	// on one element never changes another element's state.
	Decide(ctx context.Context, reqs []DecisionRequest) ([]DecisionOutcome, error)
	// Cancel withdraws a pending or approved entry.
	Cancel(ctx context.Context, requestID string) error
	// ConsumeToken redeems an approved token exactly once, against the
	// action it was issued for.
	ConsumeToken(ctx context.Context, req ConsumeRequest) (ConsumeResult, error)
	// Expire runs the rate-limited background sweep and reports how many
	// entries it retired.
	Expire(ctx context.Context) (int, error)
}

// ApprovalQueueConfig is what a queue is built from.
type ApprovalQueueConfig struct {
	// Store backs the single-use ledger. Required.
	Store provider.Store
	// Registry resolves capability names. Required.
	Registry CapabilityRegistry
	// Grants is re-consulted at redemption, so revoking the grant an
	// entry was admitted under invalidates the approval. Required.
	Grants GrantStore
	// Clock is the injected time source. Required.
	Clock Clock
	// Batching holds the R-14.29 numerics. A zero value takes the 08 §3
	// defaults; an out-of-range value is refused.
	Batching ApprovalBatching
	// Recorder is the audit sink. Optional: a queue with no recorder
	// still enforces every rule, it just writes no rows.
	Recorder ApprovalRecorder
	// DenyList is the deny-list engine. Optional; the elevation-class and
	// L4 refusals below apply with or without it.
	DenyList DenyLister
	// Minter mints tokens. Optional; a crypto/rand minter is used when it
	// is absent.
	Minter TokenMinter
}

// approvalEntry is one queued action, in daemon memory only.
type approvalEntry struct {
	seq         uint64
	requestID   string
	batchID     string
	subject     Subject
	capability  string
	level       RiskLevel
	summary     string
	actionHash  string
	paramsHash  string
	nonce       string
	issued      time.Time
	expires     time.Time
	state       ApprovalState
	grantBacked bool
}

// approvalBatch is the currently open collection window.
type approvalBatch struct {
	id       string
	openedAt time.Time
	members  []string
}

// StoreApprovals is the ApprovalQueue implementation: pending entries in
// daemon memory (they must not survive a restart — an approval nobody is
// waiting on is an approval nobody gave), and the single-use ledger in
// durable B-layer storage (a spent nonce must survive everything).
type StoreApprovals struct {
	cfg    ApprovalQueueConfig
	ledger *Ledger

	mu      sync.Mutex
	entries map[string]*approvalEntry
	order   []string
	batch   *approvalBatch
	seq     uint64
	// lastSweep rate-limits Expire to one pass a minute.
	lastSweep time.Time
	// expiredAudit stages snapshots of entries retired under the lock, so
	// their audit rows are written after it is released — a recorder must
	// never be called while the queue is locked, or a recorder that reads
	// the queue would deadlock it.
	expiredAudit []approvalEntry
}

var _ ApprovalQueue = (*StoreApprovals)(nil)

// NewApprovalQueue builds a queue from cfg.
//
// The four required collaborators are required for the same reason the
// engine's are: a queue missing one could only answer by assuming
// something about the layer it cannot consult. Without a store it cannot
// remember a spent nonce; without a registry it cannot tell whether the
// capability still exists; without a grant store it cannot notice a
// revocation; without a clock it cannot expire anything.
func NewApprovalQueue(cfg ApprovalQueueConfig) (*StoreApprovals, error) {
	if cfg.Store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval queue requires a store")
	}
	if cfg.Registry == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval queue requires a capability registry")
	}
	if cfg.Grants == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval queue requires a grant store")
	}
	if cfg.Clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval queue requires a clock")
	}
	batching, err := resolveBatching(cfg.Batching)
	if err != nil {
		return nil, err
	}
	cfg.Batching = batching
	if cfg.Minter == nil {
		cfg.Minter = randomTokenMinter{}
	}
	ledger, err := NewLedger(cfg.Store, cfg.Clock)
	if err != nil {
		return nil, err
	}
	return &StoreApprovals{
		cfg:     cfg,
		ledger:  ledger,
		entries: map[string]*approvalEntry{},
	}, nil
}

// resolveBatching fills in the 08 §3 defaults for an unset value and
// refuses an out-of-range one, on the same terms autonomy_config.go's
// parser does — so a queue built programmatically cannot hold numerics the
// config file would have been refused for.
func resolveBatching(b ApprovalBatching) (ApprovalBatching, error) {
	out := b
	if out.WindowSeconds == 0 {
		out.WindowSeconds = DefaultApprovalBatchWindowSeconds
	}
	if out.Cap == 0 {
		out.Cap = DefaultApprovalBatchCap
	}
	if out.WindowSeconds < 1 || out.WindowSeconds > maxApprovalBatchWindowSeconds {
		return ApprovalBatching{}, newConfigError("policy.approval_batch_window_s",
			"must be between 1 and %d", maxApprovalBatchWindowSeconds)
	}
	if out.Cap < 1 || out.Cap > maxApprovalBatchCap {
		return ApprovalBatching{}, newConfigError("policy.approval_batch_cap",
			"must be between 1 and %d", maxApprovalBatchCap)
	}
	return out, nil
}

// window returns the configured batch window as a duration.
func (q *StoreApprovals) window() time.Duration {
	return time.Duration(q.cfg.Batching.WindowSeconds) * time.Second
}
