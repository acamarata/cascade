// Package policy (approval_queue_enqueue.go): Purpose: the admission half
//
//	of the approval queue — what may be queued at all, what a duplicate
//	does, and how entries group into a batch.
//
// Inputs: an EnqueueRequest from the policy engine's ask-verdict path.
// Outputs: an EnqueueResult carrying the request id, the batch it joined,
//
//	whether it coalesced with an existing entry, and the daemon-memory
//	token; or a refusal.
//
// Constraints: the refusal order is the contract, and every step denies.
//
//	The elevation-class and deny-list checks come FIRST, before anything is
//	minted or recorded, because §5.14 makes those actions local-only: an
//	action that must be authorized at the machine it runs on must never
//	acquire a remote-approvable request id, not even a refused one. The
//	rung check comes next: only L2 and L3 are ask-tier, L0/L1 have nothing
//	to ask about and L4 is deny-list. The queue hashes the action and the
//	parameters ITSELF; a caller may supply a params digest, but it is
//	verified against the bytes rather than believed, because a believed
//	digest would let a caller bind an approval to something other than what
//	it is about to run.
//
// SPORT: internal/policy EnqueueRequest/ADDED, EnqueueResult/ADDED
//
//	(P1-E09-W2-S18-T3).
package policy

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EnqueueRequest is one ask-tier action asking to be queued.
type EnqueueRequest struct {
	// Subject is the principal the action runs as.
	Subject Subject
	// Capability is the registered capability the action needs.
	Capability string
	// Verb is the RPC method name, when the action is one. It is
	// classified against internal/rpc's canonical §5.14 elevated-verb
	// table; an empty verb is not an assertion that the action is safe,
	// it only means there is no verb to classify.
	Verb string
	// Level is the already-resolved rung. Must be L2 or L3.
	Level RiskLevel
	// Action is the canonical text of what will run. It is hashed, and it
	// is what a redemption is compared against.
	Action string
	// Params are the action's parameters, hashed alongside Action.
	Params []byte
	// ParamsHash is an OPTIONAL caller-supplied digest of Params. When it
	// is present it is verified against Params rather than trusted; a
	// mismatch or a malformed value is refused.
	ParamsHash string
	// Summary is the human-readable description a surface displays.
	Summary string
}

// EnqueueResult is what admission produced.
type EnqueueResult struct {
	// RequestID identifies the queued action — the existing one on a
	// deduplicated Enqueue.
	RequestID string
	// BatchID names the collection window the entry belongs to.
	BatchID string
	// Deduplicated reports that this call coalesced with an entry already
	// in the open batch; no second token was minted.
	Deduplicated bool
	// Flushed reports that the batch closed on this call, at the cap or
	// because the window elapsed, and is ready to be presented.
	Flushed bool
	// Level is the rung the entry carries.
	Level RiskLevel
	// Summary is the exact string a surface must display, rung included.
	Summary string
	// Token is the daemon-memory token. It is never returned by
	// GetPending and never crosses a bridge.
	Token ApprovalToken
}

// randomTokenMinter is the default TokenMinter: crypto/rand identifiers,
// no signing. It is a real implementation (Art.1) — the ids it mints are
// unguessable and unique — and it is the seam H/S-16.T3's Ed25519 minter
// replaces when it lands.
type randomTokenMinter struct{}

// Mint implements TokenMinter.
func (randomTokenMinter) Mint(_ context.Context, req ApprovalMintRequest) (ApprovalToken, error) {
	requestID, err := randomID()
	if err != nil {
		return ApprovalToken{}, err
	}
	nonce, err := randomID()
	if err != nil {
		return ApprovalToken{}, err
	}
	return ApprovalToken{
		RequestID:  requestID,
		Nonce:      nonce,
		ActionHash: req.ActionHash,
		ParamsHash: req.ParamsHash,
		Issued:     req.Issued,
		Expires:    req.Expires,
	}, nil
}

// randomID returns 32 hex characters of cryptographic randomness. The
// alphabet is deliberately a subset of validSubjectID's, so a minted id is
// always a well-formed storage key component.
func randomID() (string, error) {
	var buf [16]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err,
			"policy: approval queue: reading random bytes")
	}
	return hex.EncodeToString(buf[:]), nil
}

// hashApproval returns the hex digest of s. Both the action text and the
// parameter bytes go through it, so one function defines what "the same
// action" means.
func hashApproval(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Enqueue implements ApprovalQueue.
func (q *StoreApprovals) Enqueue(ctx context.Context, req EnqueueRequest) (EnqueueResult, error) {
	if q == nil {
		return EnqueueResult{}, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	if err := q.admissible(ctx, req); err != nil {
		return EnqueueResult{}, err
	}
	actionHash := hashApproval([]byte(req.Action))
	paramsHash := hashApproval(req.Params)
	if err := checkSuppliedParamsHash(req.ParamsHash, paramsHash); err != nil {
		return EnqueueResult{}, err
	}
	granted := q.grantBacked(ctx, req)

	q.mu.Lock()
	res, entry, err := q.admitLocked(ctx, req, actionHash, paramsHash, granted)
	q.mu.Unlock()
	if err != nil {
		return EnqueueResult{}, err
	}
	q.record(ctx, enqueueKind(res.Deduplicated), entry, "queued")
	return res, nil
}

// enqueueKind picks the audit kind for an admission: a coalesced call is
// approval.dedup, a fresh one approval.enqueue.
func enqueueKind(deduplicated bool) audit.Kind {
	if deduplicated {
		return audit.KindApprovalDedup
	}
	return audit.KindApprovalEnqueue
}

// admissible runs every refusal that does not need the queue's lock, in
// the order the contract fixes.
func (q *StoreApprovals) admissible(ctx context.Context, req EnqueueRequest) error {
	if err := req.Subject.Validate(); err != nil {
		return err
	}
	if _, err := q.cfg.Registry.Lookup(ctx, req.Capability); err != nil {
		return err
	}
	if err := validateApprovalText("action", req.Action); err != nil {
		return err
	}
	if err := validateApprovalText("summary", req.Summary); err != nil {
		return err
	}
	if err := q.localOnly(ctx, req); err != nil {
		return err
	}
	return askTier(req.Level)
}

// localOnly refuses the §5.14 elevation-class verbs and the deny-list.
// Both refusals are ErrLocalOnly: the caller must route the action to the
// elevation helper (D/S-07.T6), not to a remote-approvable prompt.
func (q *StoreApprovals) localOnly(ctx context.Context, req EnqueueRequest) error {
	if req.Verb != "" && rpc.IsElevated(req.Verb, nil) {
		return refuse(ErrLocalOnly,
			"%q is an elevation-class verb and is authorized locally, never by a queued approval",
			sanitize(req.Verb))
	}
	if q.cfg.DenyList == nil {
		return nil
	}
	denied, err := q.cfg.DenyList.Denied(ctx, req.Action)
	if err != nil {
		return refuse(ErrLocalOnly,
			"the deny-list could not be consulted for %q: %v", sanitize(req.Action), err)
	}
	if denied {
		return refuse(ErrLocalOnly, "%q is deny-listed", sanitize(req.Action))
	}
	return nil
}

// askTier admits L2 and L3 only. L4 is ErrLocalOnly rather than
// ErrNotAskTier because §5.15 reserves destructive/privileged actions for
// same-turn local authorization: telling the caller "wrong tier" would
// invite it to re-ask at a different rung, while telling it "local only"
// names the one correct route.
func askTier(level RiskLevel) error {
	switch safeLevel(level) {
	case L2, L3:
		return nil
	case L4:
		return refuse(ErrLocalOnly,
			"%s is destructive or privileged and is authorized locally in the same turn, never by a queued approval", L4)
	case L0, L1:
		return refuse(ErrNotAskTier,
			"%s needs no approval, so queueing it would ask the user a question with no meaning",
			safeLevel(level))
	default:
		return refuse(ErrNotAskTier, "the rung is not a member of the ladder")
	}
}

// validateApprovalText bounds one caller-supplied string and refuses
// control characters, which would let an action forge line structure in
// the surface that displays it.
func validateApprovalText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: approval queue: %s is empty", field)
	}
	if len(value) > maxApprovalTextLen {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: approval queue: %s is %d bytes, over the %d-byte limit",
			field, len(value), maxApprovalTextLen)
	}
	if sanitize(value) != value {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: approval queue: %s carries a control character or is over the display limit", field)
	}
	return nil
}

// checkSuppliedParamsHash verifies an optional caller-supplied digest
// against the one derived from the bytes. It is verified rather than
// trusted: a believed digest would let a caller bind an approval to
// parameters other than the ones it is about to run.
func checkSuppliedParamsHash(supplied, derived string) error {
	if supplied == "" {
		return nil
	}
	if len(supplied) != len(derived) {
		return refuse(ErrInvalidParamsHash,
			"the supplied params digest is %d characters, want %d", len(supplied), len(derived))
	}
	if supplied != derived {
		return refuse(ErrInvalidParamsHash,
			"the supplied params digest does not describe the parameters it came with")
	}
	return nil
}

// grantBacked records whether a standing grant covered this action at
// admission time. It is a FACT ABOUT ADMISSION, not a permission: when it
// is true, redemption re-Checks the grant store, so revoking the grant
// invalidates the approval. A store failure here reads as "not
// grant-backed", which is the restrictive reading — it can only cause the
// redemption path to skip a check that would itself have denied.
func (q *StoreApprovals) grantBacked(ctx context.Context, req EnqueueRequest) bool {
	d, err := q.cfg.Grants.Check(ctx, CheckRequest{
		Subject:    req.Subject,
		Capability: req.Capability,
	})
	return err == nil && d.Granted
}
