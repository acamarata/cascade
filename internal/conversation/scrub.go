package conversation

// Purpose: the S-44.T1 scrub pipeline -- the boundary a chat.append_turn
//   turn passes through before anything is committed, journaled or
//   mirrored over SSE. Four ordered phases over the real H/S-15
//   components: detect over the WHOLE TURN (secrets.Detector.ScanCertain
//   over the joined segments, scrub_scope.go), quarantine every hit
//   (secrets.QuarantineStore.Put), vault every hit
//   (secrets.Broker.Set), rewrite every segment
//   (secrets.Rewriter.Rewrite) -- never a self-authored stand-in for any
//   of them (R-14.283).
// Inputs: one turn's segments in order, each carrying its raw Content and
//   its Ref -- a Segment.ID, which is an address over (turnID, seq, kind)
//   and NOT over the content (domain.go's NewSegmentID), so the ref this
//   pipeline writes into the quarantine ledger cannot be inverted back to
//   a byte of the turn. That is the property
//   secrets.QuarantineEntry.SourceRef's own "never the content" contract
//   asks for; "content-addressed" would be the wrong word for it, and an
//   earlier draft of this comment used it.
// Outputs: one rewritten []byte per input segment, every detected secret
//   replaced by its typed vault-reference tag (secrets/tags.go's
//   grammar), or a non-nil error. There is no partial result: an error
//   means nothing from this call may reach storage, the SSE mirror, or a
//   future segmenter.
// Constraints: FAIL CLOSED, and PHASE-ORDERED ACROSS THE WHOLE TURN.
//   Detection and quarantine run over every segment before the first
//   vault write, and every vault write completes before the first
//   rewrite, so a refusal in a later phase never follows a partial store
//   write. A vault entry written before a later-phase refusal is INERT:
//   nothing references it (no tag reached storage, no turn was
//   forwarded), and the quarantine ledger still carries the live entry
//   that records the detection -- the append-only ledger is the record of
//   what happened, and there is no rollback call in this file. Every
//   error path emits a divergence event and forwards nothing.
//
// FALSE PREMISE, resolved (recorded per this build's process, not swept
// under a passing test). The contract's Phase 1 says "on detector error:
// quarantine + divergence". secrets.Detector.Scan/ScanCertain
// (internal/secrets/detector.go) are pure, total functions -- content in,
// []DetectionHit out, no error return, verified by reading the shipped
// code rather than assumed from the prose. There is no detector-error
// value the real component can produce. What the detect phase CAN refuse
// is a hit that straddles a segment boundary (scrub_scope.go), and what
// the quarantine phase CAN fail on is its own ledger write (disk I/O), so
// that is where this file's "on [phase] error" handling lives.
//
// SPORT: internal.conversation.scrub/ADDED (P1-E20-W5-S44-T1).

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// scrubQuarantineSubdir is the quarantine ledger's directory name under a
// caller's data directory -- the same literal cmd/cascade/vault_quarantine.go's
// quarantineDirName holds, chosen so a turn this pipeline quarantines
// shows up in the one ledger `cascade vault quarantine list` already
// reads, not a second, invisible one this package would own alone.
const scrubQuarantineSubdir = "quarantine"

// The phase names a divergence event reports. They are the four ordered
// phases of Scrub, so a subscriber reading "vault" knows detection and
// quarantine both completed for the whole turn before the refusal.
const (
	scrubPhaseDetect     = "detect"
	scrubPhaseQuarantine = "quarantine"
	scrubPhaseVault      = "vault"
	scrubPhaseRewrite    = "rewrite"
)

// divergenceNamespace/divergenceKind name the event this pipeline
// publishes on every fail-closed refusal, over C-S04.T3's bus -- the same
// EventBus seam sse.go's emitTurnAppended already publishes through, kept
// on its own namespace so a subscriber never has to inspect payload shape
// to tell a normal echo from a scrub refusal.
const (
	divergenceNamespace                  = "security"
	divergenceKind      events.EventKind = "security.scrub_divergence"
)

// ErrScrubResidualSecret reports that Rewrite either returned a tainted
// result or that re-scanning its output still found a certain-confidence
// span -- the contract's Phase 3 "fatal scrub error", refused rather than
// retried.
var ErrScrubResidualSecret = cascade.New(cascade.KindIntegrity,
	"conversation: scrub rewrite left a detectable secret span; turn refused")

// ErrScrubSegmentCountChanged reports that a ScrubPipeline returned a
// different number of segments than it was given. The pipeline in this
// file never does; the check exists because scrubSegments would otherwise
// index a short slice, and a wired-in implementation is an injected seam
// rather than something this package controls.
var ErrScrubSegmentCountChanged = cascade.New(cascade.KindIntegrity,
	"conversation: the scrub pipeline returned a different number of segments; turn refused")

// ScrubPipeline is the boundary adapter.go's handleAppendTurn scrubs a
// whole turn through before it reaches Store.AppendSegment, the journal,
// or the SSE mirror. See this file's Constraints doc comment for the
// fail-closed contract every implementation must honour.
type ScrubPipeline interface {
	// ScrubTurn returns one rewritten content slice per input segment, in
	// the same order, or an error and nothing at all.
	ScrubTurn(ctx context.Context, segs []ScrubSegment) ([][]byte, error)
}

// ScrubSegment is one segment of the turn being scrubbed: its raw bytes
// and the ref the quarantine ledger and divergence events address it by.
type ScrubSegment struct {
	// Ref is the segment's id (never its content -- see the file header).
	Ref string
	// Content is the segment's raw bytes as the caller sent them.
	Content []byte
}

// SecretVault is the write-side seam this pipeline needs from H/S-15.T1's
// broker: store a value, get back WHERE IT LANDED. An interface rather
// than *secrets.Broker directly, matching sse.go's EventBus/Substitutor
// and egress/vault.go's Vault precedent, so a test can wire a real broker
// over a temp-dir custody without this package importing a concrete
// keychain backend. *secrets.Broker satisfies it as written.
type SecretVault interface {
	Set(ctx context.Context, name string, value []byte, mode secrets.SetMode) (secrets.SetResult, error)
}

// WHY NOT secrets.Scrubber. internal/secrets offers a composed boundary,
// Scrubber.ScrubTurn, that scans and rewrites in one call, and a caller
// that only needs "make this text safe" should use it rather than wiring
// the two halves. This pipeline cannot, for two reasons that are both
// requirements here and not preferences. First, ScrubTurn runs its own
// ScanCertain over the text it is given, so calling it per segment would
// detect per segment -- and a credential split across two segments would
// be invisible to it, which is the hole turn-scoped detection exists to
// close (calling it once over the joined turn instead would rewrite across
// segment boundaries, which is worse). Second, it names each tag from the
// detector's SuggestedName, and this pipeline must name the tag from the
// vault's SetResult.Name, because a colliding Set lands under a different
// name and a tag naming the suggestion would then reference somebody
// else's secret. So the detector and rewriter are held separately here,
// with the quarantine and vault phases between them.

// TurnRewriter is H/S-15.T4's rewrite half. Production always passes
// *secrets.Rewriter, which satisfies this as written; the interface
// exists so scrub_refusal_test.go can hand the pipeline a rewriter that
// returns its input unchanged, which is the only way to drive the
// residual re-scan refusal through the real chat.append_turn RPC path
// (the real rewriter cannot be made to emit an unreplaced certain span,
// because it is given exactly the hits the same detector found).
type TurnRewriter interface {
	Rewrite(text []byte, hits []secrets.DetectionHit) (secrets.RewriteResult, error)
}

// defaultScrubPipeline is the real four-phase implementation. Build one
// with NewDefaultScrubPipelineOverVault (a composition root, over a data
// directory and an already-selected vault) or NewDefaultScrubPipelineFrom
// (tests, over already-built real collaborators).
type defaultScrubPipeline struct {
	detector   *secrets.Detector
	quarantine *secrets.QuarantineStore
	vault      SecretVault
	rewriter   TurnRewriter
	bus        EventBus
}

// NewDefaultScrubPipelineOverVault builds a real
// Detector/QuarantineStore/Rewriter pipeline over an already-selected
// vault. Selecting the custody that vault is built on is the composition
// root's job (cmd/cascade/chat_wiring.go takes it as a parameter), NOT
// this package's: a SelectCustody call in here would reach the operator's
// real OS keychain from every caller that could not pass one, which is
// exactly what internal/build's TestNoTestReachesTheRealKeychain exists
// to prevent and could not see one indirection away.
//
// dataDir is where the quarantine ledger lives (see
// scrubQuarantineSubdir), clock stamps its entries, bus carries
// divergence events.
func NewDefaultScrubPipelineOverVault(dataDir string, clock Clock, vault SecretVault, bus EventBus) (ScrubPipeline, error) {
	quarantine, err := secrets.NewQuarantineStore(filepath.Join(dataDir, scrubQuarantineSubdir), clock)
	if err != nil {
		return nil, err
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, err
	}
	return NewDefaultScrubPipelineFrom(detector, quarantine, vault, secrets.NewRewriter(), bus), nil
}

// NewDefaultScrubPipelineFrom builds a pipeline over already constructed
// collaborators -- what scrub_test.go uses to drive the real
// chat.append_turn RPC path with a real *secrets.Broker over t.TempDir()
// and a real *secrets.QuarantineStore, never a fake standing in for the
// detector, quarantine store or broker.
func NewDefaultScrubPipelineFrom(detector *secrets.Detector, quarantine *secrets.QuarantineStore,
	vault SecretVault, rewriter TurnRewriter, bus EventBus) ScrubPipeline {
	return &defaultScrubPipeline{detector: detector, quarantine: quarantine, vault: vault, rewriter: rewriter, bus: bus}
}

// ScrubTurn runs the four contract phases over the whole turn, in order.
// Detection is TURN-SCOPED (the joined segments, in order) so a secret
// split across two segments cannot hide from it; the rewrite that follows
// is per segment, because a segment is the unit that gets stored.
func (p *defaultScrubPipeline) ScrubTurn(ctx context.Context, segs []ScrubSegment) ([][]byte, error) {
	joined, bounds := joinSegments(segs)
	hits := p.detector.ScanCertain(joined)
	if len(hits) == 0 {
		return contentsOf(segs), nil
	}
	perSegment, err := splitHitsBySegment(hits, bounds)
	if err != nil {
		return nil, p.refuse(ctx, turnRef(segs), scrubPhaseDetect, err)
	}
	entries, err := p.quarantinePhase(segs, perSegment)
	if err != nil {
		return nil, p.refuse(ctx, turnRef(segs), scrubPhaseQuarantine, err)
	}
	vaulted, err := p.vaultPhase(ctx, segs, perSegment)
	if err != nil {
		// The contract's "stays quarantined": no Delete happens on this
		// path, so every entry quarantinePhase wrote remains live.
		return nil, p.refuse(ctx, turnRef(segs), scrubPhaseVault, err)
	}
	rewritten, err := p.rewritePhase(segs, vaulted)
	if err != nil {
		return nil, p.refuse(ctx, turnRef(segs), scrubPhaseRewrite, err)
	}
	p.releaseQuarantine(entries)
	return rewritten, nil
}

// scrubSegments runs ScrubPipeline over the whole turn and returns a new
// slice (segs is never mutated) with each Content replaced by its
// scrubbed bytes. A nil a.scrub (SetScrub never called, matching
// SetJournal's own optional-seam default) is a documented passthrough.
// Called from handleAppendTurn BEFORE the journal/store write and BEFORE
// emitTurnAppended -- see adapter.go's call site.
func (a *Adapter) scrubSegments(ctx context.Context, segs []Segment) ([]Segment, error) {
	if a.scrub == nil {
		return segs, nil
	}
	in := make([]ScrubSegment, len(segs))
	for i, seg := range segs {
		in[i] = ScrubSegment{Ref: seg.ID, Content: []byte(seg.Content)}
	}
	scrubbed, err := a.scrub.ScrubTurn(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(scrubbed) != len(segs) {
		return nil, ErrScrubSegmentCountChanged
	}
	out := make([]Segment, len(segs))
	for i, seg := range segs {
		seg.Content = string(scrubbed[i])
		out[i] = seg
	}
	return out, nil
}
