// Purpose: RecallWhatConversationLeg and its retrieval-owned row types,
// split out of recallwhat.go so this package holds -- in one place --
// the invariant that internal/retrieval never imports internal/conversation.
// That import used to sit directly on this interface (conversation.
// SearchFilter/TurnMatch/Segment in the method signatures); it built fine
// untagged, but internal/conversation's own -tags=integration test
// (acceptance_livemirror_test.go) imports internal/daemon, which imports
// internal/retrieval for this exact leg -- a cycle only the tagged test
// graph ever compiled. The fix is the usual seam-narrowing one: retrieval
// owns the shape the conversation leg speaks; a real conversation.Store
// is adapted to it at the composition root
// (cmd/cascade/daemon_unix_recall_what.go), which is free to import both
// packages because neither imports cmd/cascade.
//
// Inputs/Outputs: pure data-shape declarations. RecallWhatConversationLeg
// is the interface conversationOutcome (recallwhat_legs.go) calls
// against -- today only the s.conversation nil-check (D6/Q1's
// unconditional KindUnavailable refusal); these types carry the fields a
// restored leg would need once the ScopeRef widening ticket
// (s47t1-conversation-scope-ref-missing) lands and SearchTurns/
// ListSegments/ThreadPrivacy are actually invoked.
//
// Constraints: no import of internal/conversation, ever, in this file or
// anywhere else in package retrieval -- that is the invariant this file
// exists to hold (proof: `go vet -tags=integration ./internal/conversation/
// ./internal/retrieval/... ./internal/integration/`).
//
// SPORT: internal.retrieval.RecallWhatService/CHANGED (ci-fix11: broke
// the internal/conversation<->internal/retrieval integration-test import
// cycle introduced by P1-E22-W5-S47-T1).

package retrieval

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// RecallWhatTurn is the retrieval-owned mirror of the conversation.Turn
// fields a restored conversation leg needs: ID, ThreadID and Role (kept
// as a plain string -- conversation.Role is a package-local enum this
// package must not import).
type RecallWhatTurn struct {
	ID       string
	ThreadID string
	Role     string
}

// RecallWhatTurnMatch is the retrieval-owned mirror of one SearchTurns
// hit (conversation.TurnMatch): the matching turn plus its bm25() rank
// (lower is more relevant, matching SQLite's own convention).
type RecallWhatTurnMatch struct {
	Turn RecallWhatTurn
	Rank float64
}

// RecallWhatSegment is the retrieval-owned mirror of conversation.Segment,
// carrying only Content -- the one field a restored leg would hydrate a
// snippet from (recallwhat_filter.go's turnSnippet).
type RecallWhatSegment struct {
	Content string
}

// RecallWhatSearchFilter is the retrieval-owned mirror of
// conversation.SearchFilter: ThreadID == "" searches every thread,
// Limit <= 0 uses the adapted store's own default page size.
type RecallWhatSearchFilter struct {
	ThreadID string
	Limit    int
}

// RecallWhatConversationLeg is the turns/threads domain, spoken entirely
// in retrieval-owned types so this package never imports
// internal/conversation (this file's header). The composition root
// adapts a real conversation.Store to this shape.
type RecallWhatConversationLeg interface {
	SearchTurns(ctx context.Context, query string, filter RecallWhatSearchFilter) ([]RecallWhatTurnMatch, error)
	ListSegments(ctx context.Context, turnID string) ([]RecallWhatSegment, error)
	ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error)
}
