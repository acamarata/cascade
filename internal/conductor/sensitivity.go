// Purpose: the egress-enforcement layer between Router.Select (T2) and
//   the actual provider dispatch call inside Execute/ExecuteStream (T1):
//   EgressClassConductor (the H/S-16.T1 egress-inventory label for
//   conductor->provider dispatch), RegisterEgressClassConductor (the
//   idempotent registration call), the EgressSubstitutor seam and
//   SubstitutionMiddleware, the wrapper execute.go calls immediately
//   after Router.Select and before any ModelProvider dispatch.
// Inputs: the payload bytes about to leave the conductor, the caller-
//   declared SensitivityTier, and the class capability an EgressSubstitutor
//   (internal/hooks/egress.Engine in production) admits it under.
// Outputs: the substituted payload, or conductor.ErrEgressSubstitutionFailed
//   with nothing written.
// Constraints: fail closed - a substitutor error, an unknown class or a
//   disabled class never lets the original payload through. See the
//   journal for the CONTRACT DEVIATION on the pre-dispatch computed-
//   locality re-assertion R-21.228 describes: provider.Selection carries
//   no locality-bearing field and ExecutorConfig (pipeline.go, out of
//   this ticket's files_scope) grants execute.go no registry reader to
//   compute it from, so FILTER 2 (K/S-22.T2, already the first and only
//   reachable layer) is this build's sole computed-locality enforcement
//   point; both sides are quoted in the journal.
// SPORT: conductor.sensitivity/ADD (P1-E11-W3-S22-T3).

package conductor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/provider"
)

// EgressClassConductor is the H/S-16.T1 egress-inventory label for
// conductor->provider dispatch (§5.17). K/S-22.T3 is its named owner.
const EgressClassConductor egress.EgressClass = "conductor.provider-dispatch"

// egressOwner names the ticket that owns EgressClassConductor's
// registration, per the registry's Owner requirement.
const egressOwner = "P1-E11-W3-S22-T3"

// EgressSubstitutor is the egress-substitution seam SubstitutionMiddleware
// calls: exactly H/S-16.T1's internal/hooks/egress.Engine.InterceptClass
// method shape (R-40.X13's tier-explicit four-argument contract), so the
// real *egress.Engine satisfies this interface with no adapter. tier is
// an explicit caller argument, never derived from ctx or inferred from
// content.
type EgressSubstitutor interface {
	InterceptClass(ctx context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error)
}

var _ EgressSubstitutor = (*egress.Engine)(nil)

// RegisterEgressClassConductor registers EgressClassConductor on reg with
// the R-21.228 InterceptConfig: Enabled, AllowRestricted and AllowLocalOnly
// all true, AllowedTiers local-only/restricted/internal/public. A second
// registration on the same reg is treated as success (idempotent), not a
// startup failure - registration may run once per Execute/ExecuteStream
// call in a build that has no single composition-root init hook.
func RegisterEgressClassConductor(reg *egress.Registry) error {
	err := reg.Register(EgressClassConductor, egress.InterceptConfig{
		Enabled:         true,
		Owner:           egressOwner,
		AllowRestricted: true,
		AllowLocalOnly:  true,
		AllowedTiers: []egress.SensitivityTier{
			egress.TierLocalOnly, egress.TierRestricted, egress.TierInternal, egress.TierPublic,
		},
	})
	if err != nil && errors.Is(err, egress.ErrDuplicateClass) {
		return nil
	}
	return err
}

// SubstitutionMiddleware transits payload through sub under cls and tier,
// immediately after Router.Select and before any ModelProvider call. A
// substitutor error is never the original payload leaking through: it
// maps to conductor.ErrEgressSubstitutionFailed and nothing is returned.
func SubstitutionMiddleware(ctx context.Context, sub EgressSubstitutor, cls egress.EgressClass, tier egress.SensitivityTier, payload []byte) ([]byte, error) {
	out, err := sub.InterceptClass(ctx, cls, tier, payload)
	if err != nil {
		return nil, ErrEgressSubstitutionFailed
	}
	return out, nil
}

// tierToEgress converts the SDK's provider.SensitivityTier to
// internal/hooks/egress's string-keyed SensitivityTier. The two types'
// String()/literal values are identical by construction
// (restricted/local-only/internal/public); an invalid provider tier
// resolves to the fail-closed String() value, which egress's own Resolve
// narrows to TierRestricted regardless.
func tierToEgress(t provider.SensitivityTier) egress.SensitivityTier {
	return egress.SensitivityTier(t.String())
}

// substituteInputs transits req.Inputs through SubstitutionMiddleware
// under EgressClassConductor and tier, immediately after Router.Select
// and before any ModelProvider dispatch (T3). The Firewall collaborator
// is never nil once Pipeline.Ready() has passed. Registration is
// idempotent and re-attempted here since NewExecutor (pipeline.go) is
// outside this ticket's files_scope and has no single composition-root
// init hook to call it from once.
func (e *Executor) substituteInputs(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier) (provider.ModelRequest, error) {
	firewall := e.pipeline.cfg.Firewall
	if err := RegisterEgressClassConductor(firewall.Registry()); err != nil {
		return req, ErrEgressSubstitutionFailed
	}
	raw, err := json.Marshal(req.Inputs)
	if err != nil {
		return req, ErrEgressSubstitutionFailed
	}
	out, err := SubstitutionMiddleware(ctx, firewall, EgressClassConductor, tierToEgress(tier), raw)
	if err != nil {
		return req, err
	}
	var substituted []provider.ChatMessage
	if err := json.Unmarshal(out, &substituted); err != nil {
		return req, ErrEgressSubstitutionFailed
	}
	req.Inputs = substituted
	return req, nil
}
