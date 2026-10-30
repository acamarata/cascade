package delegation

// Purpose: verify a stored delegation's envelope chain before any lease
//
//	(contract:delegation-record, m-6). VerifyChain re-reads the record,
//	requires the caller's copy to equal the stored row, and walks every
//	ancestor to the root. At each level it verifies the record's Signed
//	under the pinned controller key at now, binds it to the row (ID,
//	EnvelopeDigest, ParentDigest), verifies ParentSigned the same way,
//	requires Digest(parent) == child.ParentDigest and Narrow(parent,
//	child), refuses a depth over MaxDepth or a stored depth that does not
//	follow its parent, and refuses an ID or digest seen lower in the chain
//	(cycle). The record itself must be live and below MaxLiveChildren live
//	children. Each refusal names its rule: tampering is KindIntegrity,
//	policy (depth, fan-out, narrow, expiry, state) is KindPermissionDenied.
//
// SPORT: delegation/verify/ADD (P1-AGT-11).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// VerifyChain returns rec's verified envelope when the whole stored chain
// from rec to its root verifies; see the file comment for the rules.
func VerifyChain(ctx context.Context, st *jobs.Store, rec jobs.DelegationRecord, pinned ed25519.PublicKey, now time.Time) (envelope.Envelope, error) {
	if st == nil {
		return envelope.Envelope{}, invalidf("VerifyChain: a jobs store is required")
	}
	cur, err := st.GetDelegation(ctx, rec.ID)
	if err != nil {
		return envelope.Envelope{}, ruleErr("record", err)
	}
	if !sameRecord(rec, cur) {
		return envelope.Envelope{}, integrityf("record: %q differs from its stored row", rec.ID)
	}
	if cur.State != jobs.DelegationLive {
		return envelope.Envelope{}, deniedf("state: record %q is %s, not live", cur.ID, cur.State)
	}
	w := walker{st: st, pinned: pinned, now: now, seen: map[string]bool{}}
	env, err := w.walk(ctx, cur)
	if err != nil {
		return envelope.Envelope{}, err
	}
	// Counted after the walk, so the digest is one Signed has been proven to hash to.
	n, err := st.CountLiveChildren(ctx, cur.EnvelopeDigest)
	if err != nil {
		return envelope.Envelope{}, ruleErr("fan-out", err)
	}
	if n >= MaxLiveChildren {
		return envelope.Envelope{}, deniedf("fan-out: record %q holds %d live children, MaxLiveChildren is %d", cur.ID, n, MaxLiveChildren)
	}
	return env, nil
}

// sameRecord compares the fields that bind a record to its envelope chain.
func sameRecord(a, b jobs.DelegationRecord) bool {
	return a.ID == b.ID && a.EnvelopeDigest == b.EnvelopeDigest && bytes.Equal(a.Signed, b.Signed) &&
		a.ParentDigest == b.ParentDigest && bytes.Equal(a.ParentSigned, b.ParentSigned) &&
		a.Depth == b.Depth && a.OriginPrincipal == b.OriginPrincipal && a.AgentPrincipal == b.AgentPrincipal
}

// walker carries one VerifyChain walk's inputs and the IDs and digests
// already seen lower in the chain.
type walker struct {
	st     *jobs.Store
	pinned ed25519.PublicKey
	now    time.Time
	seen   map[string]bool
}

// walk verifies cur and every ancestor and returns cur's envelope.
func (w *walker) walk(ctx context.Context, cur jobs.DelegationRecord) (envelope.Envelope, error) {
	var leaf envelope.Envelope
	for hop := 0; ; hop++ {
		if hop > MaxDepth {
			return envelope.Envelope{}, deniedf("depth: the chain is longer than MaxDepth %d", MaxDepth)
		}
		env, err := w.check(cur)
		if err != nil {
			return envelope.Envelope{}, err
		}
		if hop == 0 {
			leaf = env
		}
		if cur.ParentDigest == "" {
			if cur.Depth != 0 || len(cur.ParentSigned) != 0 {
				return envelope.Envelope{}, integrityf("chain: root record %q has depth %d or a ParentSigned", cur.ID, cur.Depth)
			}
			return leaf, nil
		}
		next, done, err := w.parent(ctx, cur, env)
		if err != nil {
			return envelope.Envelope{}, err
		}
		if done {
			return leaf, nil
		}
		cur = next
	}
}

// check verifies one record's own Signed and binds it to the row.
func (w *walker) check(cur jobs.DelegationRecord) (envelope.Envelope, error) {
	if cur.Depth < 0 || cur.Depth > MaxDepth {
		return envelope.Envelope{}, deniedf("depth: record %q has depth %d, MaxDepth is %d", cur.ID, cur.Depth, MaxDepth)
	}
	// parent() refuses a repeated ID or digest before the walk moves up, so
	// marking here is enough for the cycle rule.
	w.seen[cur.ID], w.seen[cur.EnvelopeDigest] = true, true
	env, err := w.verify(cur.Signed)
	if err != nil {
		return envelope.Envelope{}, ruleErr("verify "+cur.ID, err)
	}
	d, err := envelope.Digest(env)
	switch {
	case err != nil || d != cur.EnvelopeDigest:
		return envelope.Envelope{}, integrityf("digest: record %q's Signed does not hash to its EnvelopeDigest", cur.ID)
	case env.ID != cur.ID || env.ParentDigest != cur.ParentDigest:
		return envelope.Envelope{}, integrityf("record: %q's envelope names a different ID or parent", cur.ID)
	}
	return env, nil
}

// parent verifies cur.ParentSigned, its link to cur and Narrow, and
// returns the parent's stored record; done is true when the parent is an
// unstored root envelope (a first hop at depth 1).
func (w *walker) parent(ctx context.Context, cur jobs.DelegationRecord, env envelope.Envelope) (jobs.DelegationRecord, bool, error) {
	none := jobs.DelegationRecord{}
	penv, err := w.verify(cur.ParentSigned)
	if err != nil {
		return none, false, ruleErr("verify parent of "+cur.ID, err)
	}
	d, err := envelope.Digest(penv)
	if err != nil || d != cur.ParentDigest {
		return none, false, integrityf("digest: ParentSigned of %q does not hash to its ParentDigest", cur.ID)
	}
	if w.seen[penv.ID] || w.seen[d] {
		return none, false, integrityf("cycle: an ancestor of %q reuses a descendant's ID or digest", cur.ID)
	}
	if _, err := envelope.Narrow(penv, env); err != nil {
		return none, false, cascade.Wrap(cascade.KindPermissionDenied, err, "delegation: narrow "+cur.ID)
	}
	prec, err := w.st.GetDelegation(ctx, penv.ID)
	switch {
	case cascade.HasKind(err, cascade.KindNotFound) && penv.ParentDigest == "" && cur.Depth == 1:
		return none, true, nil
	case cascade.HasKind(err, cascade.KindNotFound):
		return none, false, integrityf("chain: the ancestor record of %q is missing", cur.ID)
	case err != nil:
		return none, false, ruleErr("record", err)
	case prec.EnvelopeDigest != d || !bytes.Equal(prec.Signed, cur.ParentSigned):
		return none, false, integrityf("chain: the stored parent of %q is not its ParentSigned", cur.ID)
	case prec.Depth+1 != cur.Depth:
		return none, false, integrityf("depth: stored depth %d of %q does not follow its parent's %d", cur.Depth, cur.ID, prec.Depth)
	}
	return prec, false, nil
}

// verify decodes one Signed and verifies it under the pinned key at now.
func (w *walker) verify(b []byte) (envelope.Envelope, error) {
	s, err := envelope.Decode(b)
	if err != nil {
		return envelope.Envelope{}, cascade.Wrap(cascade.KindIntegrity, err, "delegation: decode")
	}
	return envelope.Verify(s, w.pinned, w.now)
}

// integrityf builds a KindIntegrity error with the package prefix.
func integrityf(format string, args ...any) error {
	return cascade.Newf(cascade.KindIntegrity, "delegation: "+format, args...)
}

// deniedf builds a KindPermissionDenied error with the package prefix.
func deniedf(format string, args ...any) error {
	return cascade.Newf(cascade.KindPermissionDenied, "delegation: "+format, args...)
}
