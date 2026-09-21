// Purpose: the H/S-16.T1 egress boundary recall.what's response transits
// before a byte leaves the process: the real substitution/exclusion
// mechanism (egress.Engine.InterceptClass), never a caller-declared tier
// re-derivation. Split out of recallwhat_filter.go for the 300-line cap.
//
// Inputs: a declared SensitivityTier per row (a conversation thread's own
// privacy tier; TierInternal for domains with no per-row tier, files and
// memory) and the outbound bytes.
//
// Outputs: the substituted bytes InterceptClass returns, or a refusal.
//
// Constraints: exclusion is fail-closed and unconditional on identity — a
// row's own declared tier decides whether it may cross this class's
// boundary (EgressClassRecallWhat admits internal/public only; local-only
// and restricted content is refused for every caller, replacing the
// caller-declared-Tier trust problem the adversarial review found: a
// caller cannot assert its way past an exclusion it does not control).
//
// SPORT: internal.retrieval.RecallWhatService/CHANGED (P1-E22-W5-S47-T1
// rework).

package retrieval

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EgressSubstitutor is the egress boundary recall.what writes every
// outbound byte through. It is exactly egress.Engine.InterceptClass's own
// shape (internal/conductor/sensitivity.go's EgressSubstitutor mirrors the
// same seam for the identical reason); *egress.Engine satisfies it with
// no adapter.
type EgressSubstitutor interface {
	InterceptClass(ctx context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error)
}

var _ EgressSubstitutor = (*egress.Engine)(nil)

// egressTierFor converts a provider.SensitivityTier to its egress package
// twin by name (client.go:73's own rule: the wire and every cross-package
// boundary decode sensitivity as the tier's String() name, never its
// numeric encoding — the two enums are declared in different packages for
// different reasons and must never be compared as raw integers).
func egressTierFor(t provider.SensitivityTier) egress.SensitivityTier {
	return egress.SensitivityTier(t.String())
}

// substituteField runs one outbound string through the egress boundary at
// tier, returning the substituted value. A refusal here is reported to
// the caller via ok=false; callers decide per field whether that means
// "drop this row" (a privacy exclusion) or "fail the whole request" (an
// internal/public field the class is configured to always admit, so a
// refusal there means the firewall itself is unavailable, not that this
// one row is sensitive).
func substituteField(ctx context.Context, sub EgressSubstitutor, tier egress.SensitivityTier, s string) (string, bool) {
	if s == "" {
		return "", true
	}
	if sub == nil {
		return "", false
	}
	out, err := sub.InterceptClass(ctx, egress.EgressClassRecallWhat, tier, []byte(s))
	if err != nil {
		return "", false
	}
	return string(out), true
}

// substituteInternal runs a structural field (an error string, a rendered
// citation block, a memory snippet — nothing with a per-row privacy tier
// of its own) through the boundary at TierInternal, the tier
// EgressClassRecallWhat is always configured to admit. A refusal here
// means the firewall itself could not be consulted, which fails the whole
// request rather than a single row: an operator needs to see that, not a
// response quietly missing one field.
func substituteInternal(ctx context.Context, sub EgressSubstitutor, s string) (string, error) {
	out, ok := substituteField(ctx, sub, egress.TierInternal, s)
	if !ok {
		return "", cascade.New(cascade.KindUnavailable,
			"recall.what: the egress boundary refused an internal-tier field; the response cannot be assembled")
	}
	return out, nil
}
