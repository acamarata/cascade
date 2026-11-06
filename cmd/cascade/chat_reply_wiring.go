// Purpose: compose the conversation Replier at the daemon root and register
// chat.reply_open and chat.reply. PORTABLE: this file carries no build tag,
// like chat_wiring.go which calls it.
//
// WHY THIS FILE EXISTS. internal/conversation built the Replier
// (conversation.Replier, NewReplier, RegisterHandlers) and nothing in the
// daemon ever composed one, so chat.reply_open and chat.reply were never
// registered. The reply pipeline was tested and shipped, and never ran.
//
// THE ORDER. RecoverPending runs before chat.reply registers: a daemon
// restart must mark any pending intents as unknown outcome before a new
// client can open a fresh stream. This matches the journal resume order
// in wireChatJournal — the daemon refuses to accept new work before it
// has accounted for the old.
//
// Inputs: the RPC registry, the conversation store, the journal store
// (returned by wireChatJournal), the conductor door (satisfies ReplyDoor),
// the token counter, the effect log, the reply hub, and the clock.
// Outputs: chat.reply_open and chat.reply bound on the registry.
// Constraints: a nil journal store skips journaling with one WARN, matching
// wireChatJournal. The daemon always passes its runtime store; only test
// compositions reach the nil case.
//
// SPORT: cmd/cascade chat reply wiring (ADD) — P1-CPA-13b.
package main

import (
	"context"
	"log/slog"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireChatReply composes the conversation Replier, recovers any pending
// intents, and registers chat.reply_open and chat.reply on the registry.
//
// The journal store is the one wireChatJournal returned (the conversation
// namespace's journal). A nil journal skips journaling with one WARN and the
// replier runs without persistence — only test compositions reach this.
func wireChatReply(ctx context.Context, registry *rpc.Registry,
	convStore conversation.Store, journal conversation.JournalStore,
	door conversation.ReplyDoor, counter provider.TokenCounter,
	effects *audit.EffectLog, hub *conversation.ReplyHub, clock runtime.Clock) error {
	if journal == nil {
		slog.Warn(chatNoStoreWarning)
	}
	cfg := conversation.ReplyConfig{
		Store:   convStore,
		Journal: journal,
		Door:    door,
		Counter: counter,
		Effects: effects,
		Hub:     hub,
		Clock:   clock,
		Budget:  4096, // default budget; real value comes from config in production
	}
	replier, err := conversation.NewReplier(cfg)
	if err != nil {
		return err
	}
	// Recover pending intents BEFORE registering handlers: a restart must
	// account for in-flight work before accepting new streams.
	if err := replier.RecoverPending(ctx); err != nil {
		return err
	}
	replier.RegisterHandlers(registry)
	return nil
}
