// Purpose: the V/S-47.T2 composition-root call site
// (internal/conversation.NewResumer needs a *cascadecontext.
// Composer, the chat.* RPC registry, and an egress.Engine -- none of
// which internal/conversation may construct itself without importing
// internal/context/internal/hooks/egress at a point that would widen
// that package's own dependency surface beyond what resume.go's design
// intends). A sibling of chat_wiring.go (which calls wireChatResume with
// one line), not an edit to that file's own body, following
// chat_topics_engine.go's newChatTopicEngine precedent for the identical
// situation one ticket earlier (U/S-46.T4 D2).
//
// SCOPE NOTE: chat_wiring.go is the real composition root
// (internal/conversation.Adapter.RegisterHandlers is already called from
// there) and is not in this ticket's files_scope, but per the standing
// "composition roots are always in scope" ruling (S-48.T1/S-48.T3/
// S-51.T4/S-46.T4/S-50.T4 precedent) a unit is not done until its
// production caller exists; git status confirms chat_wiring.go is not
// held by another lane this session, so wiring it is a recorded, minimal
// scope deviation, exactly this file's own precedent's shape.
//
// Inputs: the daemon's conversation.Store and a conversation.ResumeQuerier
// (in production, the shared *rpc.Registry itself -- already carries
// recall.what once daemon_unix_recall_what.go's own composition root
// runs, registration order does not matter, Dispatch resolves the method
// map at CALL time, not at construction time).
//
// WHY THIS FILE TAKES conversation.ResumeQuerier, NOT *rpc.Registry.
// internal/client's TestCmdRPCServerBoundary_RealTreeGreen forbids any
// non-exempt cmd/cascade file from importing internal/rpc at all
// (chat_wiring.go IS on that exemption list; this sibling file is not,
// and growing that list is the LAST resort, not the first). Accepting
// the interface conversation.ResumeQuerier already declares (resume.go)
// means this file never imports internal/rpc: chat_wiring.go, which
// already imports it for wireChatHandlers' own *rpc.Registry parameter,
// passes the real registry in, and Go's implicit interface satisfaction
// does the rest -- no adapter type, no second interface.
//
// THE EGRESS ENGINE IS THE PROCESS FIREWALL'S. The resume block transits
// egress.ProcessEngine (egress_firewall.go), resolved per call so a vault
// that is not yet bound fails the resume closed instead of binding the
// default paths early; this file builds no engine of its own.
// Outputs: a *conversation.Resumer ready for
// Adapter.SetResume, or a construction error.
// Constraints: provider.NaiveTokenCounter is the ONE TokenCounter
// implementation this tree ships (internal/daemon/context_assemble.go's
// own precedent for the identical Composer construction); no second
// tokenizer is invented here. No SummarizerGetter is attached (nil) --
// Compose's own documented behavior with a nil summarizer is trim-or-
// drop, which is exactly the ticket's "budget overflow: truncated with a
// trim report, never hard-fail" contract, so there is nothing for a
// summarizer to add that this ticket's own acceptance criteria ask for.
//
// SPORT: cmd/cascade chat resume wiring (ADD) -- P1-E22-W5-S47-T2.
package main

import (
	"context"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireChatResume builds a Resumer over store, dispatching its query path
// through querier (V/S-47.T1's recall.what, once wired -- in production
// the daemon's own *rpc.Registry, passed in by chat_wiring.go) and its
// substitution pass through the process firewall's engine.
func wireChatResume(store conversation.Store, querier conversation.ResumeQuerier) (*conversation.Resumer, error) {
	composer, err := cascadecontext.NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		return nil, err
	}
	return conversation.NewResumer(store, composer, querier, newChatResumeEgressEngine())
}

// processResumeEgress is conversation.EgressSubstitutor over the process
// firewall: each call asks egress.ProcessEngine for the engine, so a
// firewall that cannot be built refuses the substitution (the resume then
// returns nothing) and the block never leaves unsubstituted.
type processResumeEgress struct{}

// InterceptClass substitutes content at class and tier through the process
// engine, or returns the engine's refusal unchanged.
func (processResumeEgress) InterceptClass(ctx context.Context, class egress.EgressClass,
	tier egress.SensitivityTier, content []byte) ([]byte, error) {
	engine, err := egress.ProcessEngine(egress.DefaultRegistry())
	if err != nil {
		return nil, err
	}
	return engine.InterceptClass(ctx, class, tier, content)
}

// newChatResumeEgressEngine is the substitutor Resume's composed context
// transits (internal/conversation/resume_egress.go).
func newChatResumeEgressEngine() conversation.EgressSubstitutor { return processResumeEgress{} }
