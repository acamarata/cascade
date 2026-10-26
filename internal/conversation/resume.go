package conversation

// Purpose: V/S-47.T2's bring-into-chat resume flow. Given a topic id (a
//   conversation.Thread id -- see resume_topic.go's IDENTITY note) or a
//   free-text query (routed to V/S-47.T1's recall.what, resume_query.go),
//   Resumer assembles the matching content into a bounded,
//   budget-enforced context block via U/S-46.T1's Composer and returns it
//   for injection as the leading context of a chat session
//   (adapter_resume.go exposes this over chat.resume; plugins/cascade-pa
//   wires the CLI flags and the /resume in-session command onto the same
//   Resume call).
// Inputs: a ResumeRequest naming exactly one source, an optional budget,
//   and the destination session's stored §5.16 tier (IntoTier, resolved
//   daemon-side by adapter_resume.go).
// Outputs: a ResumeResult carrying the substituted context text, the
//   Composer's own token accounting, and a code-fixed, content-free trim
//   report -- or one of resume_errors.go's typed refusals.
// Constraints: BOUNDED CONTEXT. Resume never calls Compose with a
//   caller-supplied budget of zero or less (Compose itself refuses that,
//   composer_core.go); DefaultResumeBudget substitutes for an unset
//   ("<=0 means unset") request budget. PRIVACY: the composed text
//   ALWAYS transits resume_egress.go's substitution pass before it is
//   returned -- there is no code path that skips it (see
//   ErrResumeSubstitutionUnavailable). IDEMPOTENCE: Resume performs no
//   write of its own (no AppendTurn, no thread mutation) -- it is a
//   pure read-assemble-substitute pipeline over already-committed
//   content, so calling it twice with the same request and the same
//   underlying data returns the same result; adapter_resume.go's own doc
//   comment covers the caller-visible half of this (repeat calls never
//   double-inject a leading block into a session).
// SPORT: internal.conversation.resume/ADDED (P1-E22-W5-S47-T2).

import (
	"context"
	"strconv"
	"strings"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DefaultResumeBudget is the token budget Resume uses when
// ResumeRequest.Budget is unset (<=0). Sized generously enough to hold a
// short thread or a handful of recall.what rows whole, while still small
// enough that the bounded-context invariant (Compose's own contract) is
// exercised, not merely assumed, by real resume calls.
const DefaultResumeBudget = 4000

// ResumeRequest is one bring-into-chat resume call.
type ResumeRequest struct {
	// TopicID resumes a specific conversation thread. Mutually exclusive
	// with Query -- exactly one must be non-empty.
	TopicID string
	// Query is routed to recall.what (resume_query.go). Mutually
	// exclusive with TopicID.
	Query string
	// Budget is the token budget Compose assembles within. <=0 means
	// unset; DefaultResumeBudget applies.
	Budget int
	// IntoTier is the destination session thread's STORED §5.16 tier
	// (adapter_resume.go resolves it from session_thread_id; the wire
	// never carries a tier). The source-versus-target rule compares every
	// resumed thread against it (resume_topic.go).
	IntoTier provider.SensitivityTier
	// Cwd is this request's session working directory, forwarded to
	// recall.what's scope resolution on the Query path only
	// (resume_query.go): it is the ONE raw session signal that actually
	// changes scope resolution (internal/context/scope/resolver.go: only
	// Cwd drives the GitRoot lookup; every other ResolveInput field is
	// passed through unexamined), matching `cascade recall what`'s own
	// real client (cmd/cascade/recall_what.go: WhatParams{Cwd: cwd}).
	Cwd string
	// Admits is the caller's thread view (CallerThreadView): a source
	// thread it refuses reads as not found. Nil admits every thread (the
	// owner).
	Admits func(threadID string) bool
}

// ResumeResult is Resume's return value.
type ResumeResult struct {
	// ThreadID is the resumed SOURCE thread: the topic id, or the thread
	// the query path resolved.
	ThreadID string
	// Context is the composed, egress-substituted context block ready to
	// inject as the session's leading context.
	Context string
	// TokensUsed/Budget echo Compose's own accounting (ComposedResult),
	// so a caller can independently verify the bounded-context invariant.
	TokensUsed int
	Budget     int
	// Trimmed is true when at least one BudgetTrimEvent fired -- the
	// budget-overflow acceptance criterion's signal to emit a warning
	// and still open the session, never to hard-fail.
	Trimmed bool
	// TrimReport is one fixed, code-chosen line per BudgetTrimEvent
	// (never slot content -- composer_types.go's own Label/Reason rule).
	// A caller (plugins/cascade-pa) writes these to stderr; this package
	// never performs I/O itself (internal/build/outputgate.go's
	// boundary).
	TrimReport []string
}

// ResumeStore is the read-only Store surface Resume needs. The full Store
// (store.go) satisfies it with no adapter; declared narrower so a test can
// supply a four-method double. Resume never writes.
type ResumeStore interface {
	GetThread(ctx context.Context, id string) (Thread, bool, error)
	ListTurns(ctx context.Context, threadID string) ([]Turn, error)
	ListSegments(ctx context.Context, turnID string) ([]Segment, error)
	ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error)
}

// resumeComposer narrows *cascadecontext.Composer to the one method
// Resume calls. Declared locally (matching composer_types.go's own
// SummarizerGetter precedent for a package that depends on internal/
// context's SHAPE, not a concrete construction path) so resume_test.go
// can supply a deterministic double instead of a real Composer.
type resumeComposer interface {
	Compose(ctx context.Context, budget int, slots []cascadecontext.Slot) (cascadecontext.ComposedResult, error)
}

// ResumeQuerier dispatches V/S-47.T1's recall.what method IN-PROCESS --
// see resume_query.go's own doc comment for why this is "internal RPC"
// and why this package cannot import internal/retrieval directly.
// *internal/rpc.Registry satisfies this with no adapter.
type ResumeQuerier interface {
	Dispatch(ctx context.Context, req *rpc.Request) (any, *rpc.ErrorObject)
}

// Resumer is V/S-47.T2's resume service. The zero value is not
// usable; construct with NewResumer.
type Resumer struct {
	store    ResumeStore
	composer resumeComposer
	querier  ResumeQuerier
	subst    EgressSubstitutor
}

// NewResumer returns a Resumer. store and composer
// are required (nil refuses construction outright -- there is no useful
// degraded mode for either). querier may be nil: only the query path
// needs it, and a nil querier fails that ONE path closed
// (ErrResumeQuerierUnavailable) rather than the whole service refusing
// to construct, so a build that has not wired recall.what yet still
// serves topic-id resume. subst may be nil at construction for the same
// reason tests need it, but every real Resume call with non-empty
// composed text refuses closed on a nil subst (resume_egress.go) -- see
// ErrResumeSubstitutionUnavailable's own doc comment for why this differs
// from store/composer's outright construction refusal.
func NewResumer(store ResumeStore, composer resumeComposer, querier ResumeQuerier, subst EgressSubstitutor) (*Resumer, error) {
	if store == nil {
		return nil, errResumeNilStore()
	}
	if composer == nil {
		return nil, errResumeNilComposer()
	}
	return &Resumer{store: store, composer: composer, querier: querier, subst: subst}, nil
}

// Resume runs one bring-into-chat resume call: resolve the source
// (topic-id or query), compose it within budget, substitute the result
// through the egress boundary, and return it. See this file's own
// BOUNDED CONTEXT/PRIVACY/IDEMPOTENCE constraints above.
func (r *Resumer) Resume(ctx context.Context, req ResumeRequest) (ResumeResult, error) {
	if err := validateResumeRequest(req); err != nil {
		return ResumeResult{}, err
	}
	budget := req.Budget
	if budget <= 0 {
		budget = DefaultResumeBudget
	}

	threadID, tier, slots, err := r.resolveSource(ctx, req)
	if err != nil {
		return ResumeResult{}, err
	}

	composed, err := r.composer.Compose(ctx, budget, slots)
	if err != nil {
		return ResumeResult{}, errResumeComposeFailed(err)
	}

	text := joinComposedSlots(composed.Slots)
	substituted, err := substituteResumeContext(ctx, r.subst, tier, text)
	if err != nil {
		return ResumeResult{}, err
	}

	return ResumeResult{
		ThreadID:   threadID,
		Context:    substituted,
		TokensUsed: composed.TokensUsed,
		Budget:     composed.Budget,
		Trimmed:    len(composed.Trims) > 0,
		TrimReport: trimReportLines(composed.Trims),
	}, nil
}

// resolveSource resolves req (already validated as naming exactly one
// source) to its SOURCE threads -- the topic id, or the threads the query
// path resolved (resume_query.go) -- and resumes them through
// resolveThreads, so both paths apply the same source-versus-target rule.
// It returns the primary (first) thread id and the contributing threads'
// joined tier, at which resume_egress.go transits the composed text.
func (r *Resumer) resolveSource(ctx context.Context, req ResumeRequest) (string, provider.SensitivityTier, []cascadecontext.Slot, error) {
	threadIDs := []string{req.TopicID}
	if req.TopicID == "" {
		resolved, err := r.resolveQuery(ctx, req.Query, req.Cwd, req.Admits)
		if err != nil {
			return "", 0, nil, err
		}
		threadIDs = resolved
	}
	tier, slots, err := r.resolveThreads(ctx, threadIDs, req.IntoTier, req.Admits)
	if err != nil {
		return "", 0, nil, err
	}
	return threadIDs[0], tier, slots, nil
}

// validateResumeRequest enforces "exactly one of topic_id or query".
func validateResumeRequest(req ResumeRequest) error {
	hasTopic := strings.TrimSpace(req.TopicID) != ""
	hasQuery := strings.TrimSpace(req.Query) != ""
	if hasTopic == hasQuery { // both false, or both true
		return ErrResumeInvalidRequest
	}
	if !req.IntoTier.Valid() { // an out-of-range tier ranks as most restrictive for a source but would admit every source as a target
		return cascade.Newf(cascade.KindInvalidInput, "conversation: resume: %q is not a sensitivity tier", req.IntoTier.String())
	}
	return nil
}

// joinComposedSlots concatenates every surviving ComposedSlot's Content,
// double-newline separated, in the order Compose returned them (the same
// priority order Resume supplied). This is Resume's ENTIRE assembly
// format -- no headers, no per-slot framing -- so the substitution pass
// (resume_egress.go) runs over exactly the bytes a client will see, with
// nothing this package added going unscanned.
func joinComposedSlots(slots []cascadecontext.ComposedSlot) string {
	parts := make([]string, 0, len(slots))
	for _, s := range slots {
		if s.Content == "" {
			continue
		}
		parts = append(parts, s.Content)
	}
	return strings.Join(parts, "\n\n")
}

// trimReportLines converts Compose's own BudgetTrimEvents into fixed,
// content-free report lines: "<label>: <reason>" (dropped) or
// "<label>: <reason> (kept N tokens)" (trimmed to a shorter substitute).
// Kind/Label/Reason are all code-fixed or caller-chosen identifiers
// (composer_types.go's own rule) -- never slot Content.
func trimReportLines(trims []cascadecontext.BudgetTrimEvent) []string {
	if len(trims) == 0 {
		return nil
	}
	lines := make([]string, 0, len(trims))
	for _, t := range trims {
		if t.Dropped {
			lines = append(lines, t.Label+": "+t.Reason)
			continue
		}
		lines = append(lines, t.Label+": "+t.Reason+" (kept "+strconv.Itoa(t.KeptTokens)+" tokens)")
	}
	return lines
}
