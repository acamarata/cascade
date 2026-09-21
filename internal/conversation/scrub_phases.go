package conversation

// Purpose: the scrub pipeline's four phase bodies and its divergence
//   emitter, split from scrub.go under Art.10.3's 300-line file cap. The
//   ORDER these run in, and why a later refusal is safe, is scrub.go's
//   ScrubTurn and its Constraints doc comment; this file is what each
//   phase does.
// SPORT: internal.conversation.scrub/ADDED (P1-E20-W5-S44-T1).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// quarantinePhase records every hit in every segment before any vault
// write runs.
func (p *defaultScrubPipeline) quarantinePhase(segs []ScrubSegment,
	perSegment [][]secrets.DetectionHit) ([]secrets.QuarantineEntry, error) {
	var entries []secrets.QuarantineEntry
	for i, hits := range perSegment {
		for _, hit := range hits {
			entry, err := p.quarantine.Put(hit, segs[i].Ref, spanOf(segs[i].Content, hit))
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// vaultPhase stores each hit's actual bytes and returns the hits with
// SuggestedName replaced by the name the vault ACTUALLY used.
//
// secrets.SetRename, never SetUpdate: an operator's existing
// OPENAI_API_KEY must not be overwritten by whatever a chat turn happened
// to contain, and two hits in one turn that suggest the same name are two
// different secrets that need two different vault entries. The broker
// reports where each landed (SetResult.Name), and that name -- not the
// detector's suggestion -- is what the tag carries, so the tag is always
// a reference to the entry holding THIS span's value.
func (p *defaultScrubPipeline) vaultPhase(ctx context.Context, segs []ScrubSegment,
	perSegment [][]secrets.DetectionHit) ([][]secrets.DetectionHit, error) {
	out := make([][]secrets.DetectionHit, len(perSegment))
	for i, hits := range perSegment {
		named := make([]secrets.DetectionHit, 0, len(hits))
		for _, hit := range hits {
			res, err := p.vault.Set(ctx, hit.SuggestedName, spanOf(segs[i].Content, hit), secrets.SetRename)
			if err != nil {
				return nil, err
			}
			hit.SuggestedName = res.Name
			named = append(named, hit)
		}
		out[i] = named
	}
	return out, nil
}

// rewritePhase rewrites each segment's hits into their tags, then
// verifies no certain-confidence span survived -- per segment, and once
// more over the rejoined turn, which is where a span formed across a
// segment boundary by the rewrite itself would show up.
//
// result.Tainted is not re-checked: secrets.Rewriter.Rewrite returns
// Tainted true on EVERY path that also returns a non-nil error and false
// on its only nil-error path (failedRewrite sets both together) --
// verified by reading the shipped function. A `if result.Tainted` after
// the err check can never be true, and this file does not carry a check
// that cannot fail.
func (p *defaultScrubPipeline) rewritePhase(segs []ScrubSegment,
	perSegment [][]secrets.DetectionHit) ([][]byte, error) {
	out := make([][]byte, len(segs))
	for i, seg := range segs {
		if len(perSegment[i]) == 0 {
			out[i] = seg.Content
			continue
		}
		result, err := p.rewriter.Rewrite(seg.Content, perSegment[i])
		if err != nil {
			return nil, err
		}
		if residual := p.detector.ScanCertain(result.Text); len(residual) != 0 {
			return nil, ErrScrubResidualSecret
		}
		out[i] = result.Text
	}
	if residual := p.detector.ScanCertain(bytes.Join(out, nil)); len(residual) != 0 {
		return nil, ErrScrubResidualSecret
	}
	return out, nil
}

// releaseQuarantine retires every recorded entry as promoted. A release
// failure is not surfaced as a ScrubTurn error: the turn's rewritten
// bytes are already correct and safe to forward, and the entry stays
// visible (live) in the ledger rather than silently vanishing, so a
// failed release is a stricter, not a weaker, outcome.
func (p *defaultScrubPipeline) releaseQuarantine(entries []secrets.QuarantineEntry) {
	for _, entry := range entries {
		_ = p.quarantine.Delete(entry.ID, secrets.ReleasePromoted)
	}
}

// scrubDivergencePayload is the divergence event's wire shape: which
// phase refused and why, in a static, content-free reason string. Never
// a field that could carry a byte of the turn.
type scrubDivergencePayload struct {
	Phase  string `json:"phase"`
	Reason string `json:"reason"`
}

// refuse emits a best-effort divergence event (a publish failure does not
// mask cause) and returns the fail-closed error handleAppendTurn
// propagates instead of forwarding the turn.
//
// The CAUSE'S KIND is kept, not replaced: a straddling span is a policy
// refusal, a residual span is an integrity failure, and a ledger or vault
// failure is unavailability. Those reach the caller as different JSON-RPC
// codes, and flattening them all to one would tell an operator that a
// refusal on principle was a transient outage they could retry.
func (p *defaultScrubPipeline) refuse(ctx context.Context, sourceRef, phase string, cause error) error {
	if p.bus != nil {
		payload, _ := json.Marshal(scrubDivergencePayload{Phase: phase, Reason: cause.Error()})
		_, _ = p.bus.Publish(ctx, divergenceNamespace, divergenceKind, sourceRef, payload)
	}
	kind, ok := cascade.KindOf(cause)
	if !ok {
		kind = cascade.KindUnavailable
	}
	return cascade.Wrapf(kind, cause,
		"conversation: scrub pipeline refused the turn in the %s phase; not forwarded", phase)
}
