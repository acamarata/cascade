package sync

import (
	"context"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the one entry point a caller uses to merge a
//
//	domain — the thing that turns five strategies and a table into a
//	decision a running program can make.
//
// WHY IT IS ONE FUNCTION AND NOT FIVE EXPORTED MERGES. A caller choosing
//
//	its own strategy is a caller that can choose the wrong one, and the
//	wrong one here means (say) an append merge over accounts metadata,
//	which would keep a record the server had deleted. The strategy is
//	resolved from the registry, not passed in.
//
// THE ELIGIBILITY GATE RUNS FIRST, before any merge. Merging and then
//
//	filtering would produce the right result and the wrong journal: an
//	operator would see conflicts resolved for records that were never
//	going to be sent to that peer at all.
//
// Inputs: the domain, the peer's trust tier, and the two sides.
// Outputs: the merged state, with every decision journaled.
// SPORT: internal/sync merge application (ADD) — P1-E17-W4-S38-T2.

// MergeRequest is one domain's merge.
type MergeRequest struct {
	// Domain and Subkind name what is being merged.
	Domain  storage.DomainID
	Subkind string
	// PeerTier is the trust tier of the peer on the other side. The merge
	// is refused outright when that tier may not sync this domain.
	PeerTier nodes.Tier
	// Server is the authoritative side for the server-primary strategies,
	// and simply "the other side" for the symmetric ones.
	Server map[string]Record
	// Local is this device's side.
	Local map[string]Record
	// Blobs replaces Server/Local for the blob domain, whose union is
	// keyed by content address rather than by record id.
	ServerBlobs map[string]BlobRef
	LocalBlobs  map[string]BlobRef
	// Repo, Remote and Ref are the phase-state domain's inputs.
	Repo, Remote, Ref string
}

// MergeResult is what one merge produced.
type MergeResult struct {
	// Strategy names which merge ran, so a caller's own logs say what
	// decided rather than only what the decision was.
	Strategy StrategyName
	// Records is the merged state for the record domains.
	Records map[string]Record
	// Blobs is the merged set for the blob domain.
	Blobs map[string]BlobRef
	// Carry is the outcome for the phase-state domain.
	Carry CarryResult
}

// Merge applies the strategy this domain is registered with.
func (e *Engine) Merge(ctx context.Context, req MergeRequest) (MergeResult, error) {
	strategy, mapped := StrategyFor(req.Domain, req.Subkind)
	if !mapped || strategy == StrategyNone {
		return MergeResult{Strategy: StrategyNone}, errDomainNeverSyncs(req.Domain, req.Subkind)
	}
	if !EligibleForTier(req.Domain, req.Subkind, req.PeerTier) {
		return MergeResult{Strategy: strategy}, errTierMaySyncNothingHere(req.Domain, req.Subkind, req.PeerTier)
	}
	dc, _ := Lookup(req.Domain, req.Subkind)

	switch strategy {
	case StrategyServerPrimaryLWW:
		return MergeResult{
			Strategy: strategy,
			Records:  MergeServerPrimary(e.Conflicts(), dc, req.Server, req.Local),
		}, nil
	case StrategyMetadataOnly:
		return MergeResult{
			Strategy: strategy,
			Records:  MergeMetadataOnly(e.Conflicts(), dc, req.Server, req.Local),
		}, nil
	case StrategyAppendTombstone:
		return MergeResult{
			Strategy: strategy,
			Records:  MergeAppend(e.Conflicts(), dc, req.Server, req.Local),
		}, nil
	case StrategyContentAddressUnion:
		blobs, err := MergeContentAddressed(req.ServerBlobs, req.LocalBlobs)
		return MergeResult{Strategy: strategy, Blobs: blobs}, err
	case StrategyGitCarried:
		return e.carry(ctx, dc, strategy, req)
	case StrategyNone:
		// Unreachable: the guard above already returned. Listed so a
		// sixth strategy added without visiting this switch fails to
		// compile rather than falling through to a merge.
		return MergeResult{Strategy: strategy}, errDomainNeverSyncs(req.Domain, req.Subkind)
	default:
		return MergeResult{Strategy: strategy}, errDomainNeverSyncs(req.Domain, req.Subkind)
	}
}

// carry runs the phase-state leg, refusing when no git runner is wired.
//
// A refusal rather than a silent skip: a sync that reported success while
// carrying no phase state would leave the two machines disagreeing about
// what the work IS, which is the disagreement that matters most.
func (e *Engine) carry(
	ctx context.Context, dc DomainClass, strategy StrategyName, req MergeRequest,
) (MergeResult, error) {
	if e.git == nil {
		return MergeResult{Strategy: strategy}, cascade.New(cascade.KindUnavailable,
			"sync: phase state cannot be carried: no git runner is wired, and carriage is git's job")
	}
	carry, err := CarryPhaseState(ctx, e.git, e.Conflicts(), dc, req.Repo, req.Remote, req.Ref)
	return MergeResult{Strategy: strategy, Carry: carry}, err
}

// errDomainNeverSyncs refuses a merge for a domain that does not sync.
func errDomainNeverSyncs(domain storage.DomainID, subkind string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"sync: %s/%s is not a synced domain, so there is nothing to merge; "+
			"a domain syncs only when the registry says so", domain, subkind)
}

// errTierMaySyncNothingHere refuses a merge for a peer that may not have
// this domain at all.
func errTierMaySyncNothingHere(domain storage.DomainID, subkind string, tier nodes.Tier) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"sync: a peer at tier %q may not sync %s/%s; merging first and filtering afterwards "+
			"would journal conflicts for records that were never going to be sent", tier, domain, subkind)
}
