// Purpose: compose the chat turn journal at the daemon root and resume every
// journaled thread before chat.* registers. PORTABLE: this file carries no
// build tag, like chat_wiring.go which calls it.
//
// WHY THIS FILE EXISTS. internal/conversation built the turn journal
// (AppendTurnJournaled, Resume) and Adapter.SetJournal, and nothing in the
// daemon ever called SetJournal, so a chat.append_turn committed its turn
// without the intent/ack entries a crash resume replays. The journal was
// tested and shipped, and never ran.
//
// THE NAMESPACE. Chat entries live under chatJournalNamespace, not under
// journal.DefaultNamespace: the fleet resume scan (wireResumeScan) lists the
// default namespace's entities and would otherwise find chat threads it has
// no handler for, and a namespace purge never touches the conversation
// event rows (those live in their own tables).
//
// THE ORDER. Resume re-applies every ACKed TurnAppend into the conversation
// store, so it runs before the adapter registers chat.*: no client can read a
// thread the journal still owes rows to. A Resume failure is returned as
// KindIntegrity naming the thread; wireChatHandlers hands it up unchanged, no
// chat.* method is registered, and runDaemonWiring stops naming this entry,
// which is the refuse-to-start path.
//
// Inputs: the adapter and conversation store wireChatHandlers built, the
// daemon's runtime provider.Store, and the injected clock.
// Outputs: the one conversation.JournalStore of the chat namespace, set on
// the adapter (nil when no runtime store was given).
// Constraints: a nil store skips journaling with one slog WARN. The daemon
// always passes its runtime store; only test compositions reach the nil case.
//
// SPORT: cmd/cascade chat journal wiring (ADD) — P1-CPA-08.
package main

import (
	"context"
	"log/slog"
	"sort"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// chatJournalNamespace is the journal namespace every chat turn entry lives
// under. It is not journal.DefaultNamespace, so the fleet resume scan never
// replays chat entries.
const chatJournalNamespace = "conversation.journal"

// chatNoStoreWarning is the one line a store-less composition logs.
const chatNoStoreWarning = "chat: no runtime store; turns are not journaled"

// wireChatJournal builds the chat journal over store, resumes every journaled
// thread into convStore, and sets the journal on adapter. The caller registers
// chat.* only after this returns nil.
//
// A nil store skips journaling (one WARN) and returns a nil JournalStore. A
// failed resume returns KindIntegrity naming the thread; the adapter is left
// without a journal and the caller must not register handlers.
func wireChatJournal(ctx context.Context, adapter *conversation.Adapter, convStore conversation.Store,
	store provider.Store, clock runtime.Clock) (conversation.JournalStore, error) {
	if store == nil {
		slog.Warn(chatNoStoreWarning)
		return nil, nil
	}
	js := journal.New(store, clock, chatJournalNamespace)
	threads, err := js.ListEntities(ctx)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "chat: list journaled threads")
	}
	sort.Strings(threads)
	for _, id := range threads {
		if _, err := conversation.Resume(ctx, js, convStore, id); err != nil {
			return nil, cascade.Wrapf(cascade.KindIntegrity, err, "chat: resume journaled thread %s", id)
		}
	}
	adapter.SetJournal(js)
	return js, nil
}
