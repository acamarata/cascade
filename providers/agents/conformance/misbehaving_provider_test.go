// Purpose: misbehavingProvider — a conformingProvider wrapper that lets
//
//	exactly one method be replaced with a single flawed behavior at a time,
//	so each constructor below breaks exactly ONE guarantee the suite
//	asserts and leaves everything else genuine (inherited unchanged from
//	conformingProvider). isolated_test.go runs the one case naming that
//	guarantee against each of these, isolated from `go test`'s own
//	pass/fail state, and asserts the case correctly FAILS — this package's
//	standing, automated form of the AGENT-BRIEF's break-one-guarantee
//	mutation proof, pointed at eleven distinct guarantees instead of one,
//	never needing a manual revert because nothing here mutates shipped
//	code, only a test-only double.
//
// Constraints: exists ONLY in this _test.go file (Art.1.1); no driver ships
//
//	from this ticket.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/provider"
)

// misbehavingProvider wraps a real conformingProvider and, for whichever of
// these function fields is non-nil, substitutes it for the real method.
// Every method not overridden delegates to the embedded, genuine
// implementation.
type misbehavingProvider struct {
	*conformingProvider
	spawn     func(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error)
	cancel    func(context.Context, provider.AgentJobID) error
	message   func(context.Context, provider.AgentJobID, string) error
	status    func(context.Context, provider.AgentJobID) (provider.AgentRunState, error)
	collect   func(context.Context, provider.AgentJobID) (provider.CollectResult, error)
	artifacts func(context.Context, provider.AgentJobID) ([]string, error)
}

func (p *misbehavingProvider) Spawn(ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	if p.spawn != nil {
		return p.spawn(ctx, spec)
	}
	return p.conformingProvider.Spawn(ctx, spec)
}

func (p *misbehavingProvider) Cancel(ctx context.Context, id provider.AgentJobID) error {
	if p.cancel != nil {
		return p.cancel(ctx, id)
	}
	return p.conformingProvider.Cancel(ctx, id)
}

func (p *misbehavingProvider) Message(ctx context.Context, id provider.AgentJobID, turn string) error {
	if p.message != nil {
		return p.message(ctx, id, turn)
	}
	return p.conformingProvider.Message(ctx, id, turn)
}

func (p *misbehavingProvider) Status(ctx context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	if p.status != nil {
		return p.status(ctx, id)
	}
	return p.conformingProvider.Status(ctx, id)
}

func (p *misbehavingProvider) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	if p.collect != nil {
		return p.collect(ctx, id)
	}
	return p.conformingProvider.Collect(ctx, id)
}

func (p *misbehavingProvider) Artifacts(ctx context.Context, id provider.AgentJobID) ([]string, error) {
	if p.artifacts != nil {
		return p.artifacts(ctx, id)
	}
	return p.conformingProvider.Artifacts(ctx, id)
}

var _ provider.AgentProvider = (*misbehavingProvider)(nil)

// newSpawnEmptyJobIDProvider breaks Spawn's "non-empty JobID" guarantee.
func newSpawnEmptyJobIDProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		spawn: func(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
			return provider.SpawnResult{JobID: "", ProcessGroupID: 1}, nil
		}}
}

// newSpawnNegativePgidProvider breaks Spawn's "non-negative
// ProcessGroupID" guarantee.
func newSpawnNegativePgidProvider() *misbehavingProvider {
	n := 0
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		spawn: func(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
			n++
			return provider.SpawnResult{JobID: provider.AgentJobID(fmt.Sprintf("neg-%d", n)), ProcessGroupID: -1}, nil
		}}
}

// newSpawnDuplicatePgidProvider breaks distinct process-group identity
// across two spawns.
func newSpawnDuplicatePgidProvider() *misbehavingProvider {
	n := 0
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		spawn: func(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
			n++
			return provider.SpawnResult{JobID: provider.AgentJobID(fmt.Sprintf("dup-%d", n)), ProcessGroupID: 500}, nil
		}}
}

// newCancelNoopProvider breaks Cancel: it reports success but changes
// nothing observable through Status, and never refuses a missing job id.
func newCancelNoopProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		cancel: func(context.Context, provider.AgentJobID) error { return nil }}
}

// newMessageAlwaysOKProvider breaks Message's ErrJobNotFound refusal for a
// missing job id.
func newMessageAlwaysOKProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		message: func(context.Context, provider.AgentJobID, string) error { return nil }}
}

// newStatusInvalidProvider breaks Status's closed AgentRunState vocabulary.
func newStatusInvalidProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		status: func(context.Context, provider.AgentJobID) (provider.AgentRunState, error) {
			return provider.AgentRunState("not-a-real-state"), nil
		}}
}

// newCollectBeforeSpawnOKProvider breaks Collect's refusal of a job id that
// was never spawned.
func newCollectBeforeSpawnOKProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		collect: func(context.Context, provider.AgentJobID) (provider.CollectResult, error) {
			return provider.CollectResult{}, nil
		}}
}

// newArtifactsNilProvider breaks Artifacts' non-nil-slice guarantee.
func newArtifactsNilProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		artifacts: func(context.Context, provider.AgentJobID) ([]string, error) {
			return nil, nil
		}}
}

// newEntitledButSpawnErrorsProvider declares programmatic entitlement (the
// embedded conformingProvider's default) but Spawn refuses anyway with an
// unrelated error, breaking the "entitled means Spawn succeeds" half of
// the entitlement consistency check.
func newEntitledButSpawnErrorsProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProvider(),
		spawn: func(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
			return provider.SpawnResult{}, provider.ErrPlatform
		}}
}

// newWrongEntitlementErrorProvider declares no programmatic entitlement
// but Spawn refuses with the wrong sentinel, breaking the "refusal is
// exactly ErrEntitlement" half of the same check.
func newWrongEntitlementErrorProvider() *misbehavingProvider {
	return &misbehavingProvider{conformingProvider: newConformingProviderNotEntitled(),
		spawn: func(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
			return provider.SpawnResult{}, provider.ErrPlatform
		}}
}
