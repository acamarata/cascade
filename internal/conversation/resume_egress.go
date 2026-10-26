package conversation

// Purpose (this file): the H/S-16.T1 egress boundary Resume's composed
//   context block transits before a byte reaches the JSON-RPC response --
//   the real substitution mechanism (egress.Engine.InterceptClass), never
//   a caller-declared tier re-derivation, matching
//   internal/retrieval/recallwhat_egress.go's identical, already-shipped
//   pattern for the same underlying problem (fused, cross-domain content
//   leaving a process boundary).
// Inputs: the composed, joined context text (resume.go's joinComposedText)
//   and the SOURCE content's own §5.16 tier (resolveSource's second
//   return value).
// Outputs: the substituted text InterceptClass returns, or a refusal.
// Constraints: EgressClassConversationResume admits all four tiers
//   (internal/hooks/egress/classes.go): this class's job is unconditional
//   vault substitution (the exact-value and detector passes), never a
//   second privacy gate. Placement is decided before this file runs, by
//   the source-versus-target rule in resume_topic.go. The SOURCE thread's
//   own tier is still what InterceptClass receives, never a fixed one, so
//   a later narrowing of the class's AllowedTiers takes effect on the real
//   tier.
// SPORT: internal.conversation.resume/ADDED (P1-E22-W5-S47-T2).

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/provider"
)

// EgressSubstitutor is the egress boundary Resume writes its composed
// context block through. It is exactly egress.Engine.InterceptClass's own
// shape (internal/retrieval/recallwhat_egress.go's EgressSubstitutor and
// internal/conductor/sensitivity.go's own seam mirror the identical
// shape); *egress.Engine satisfies it with no adapter.
type EgressSubstitutor interface {
	InterceptClass(ctx context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error)
}

var _ EgressSubstitutor = (*egress.Engine)(nil)

// substituteResumeContext runs text through sub at
// EgressClassConversationResume, at tier (the SOURCE thread's own §5.16
// tier -- resolveSource's return value, never a fixed one). Empty
// text is a no-op (nothing to substitute, nothing to refuse); nil sub is
// ErrResumeSubstitutionUnavailable rather than a silent passthrough --
// see ErrResumeSubstitutionUnavailable's own doc comment for why this
// differs from sse.go's Substitutor. A non-nil error from InterceptClass
// is ALWAYS reported as the fixed ErrResumeSubstitutionFailed, never
// interpolated: the underlying error ran over conversation content and
// could otherwise echo a fragment of it back.
func substituteResumeContext(ctx context.Context, sub EgressSubstitutor, tier provider.SensitivityTier, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	if sub == nil {
		return "", ErrResumeSubstitutionUnavailable
	}
	out, err := sub.InterceptClass(ctx, egress.EgressClassConversationResume, tier, []byte(text))
	if err != nil {
		return "", ErrResumeSubstitutionFailed
	}
	return string(out), nil
}
