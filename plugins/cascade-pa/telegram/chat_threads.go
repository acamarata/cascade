// Purpose (this file): how TelegramBridge finds a chat's conversation
//   thread. Inbound, a mapped chat uses its bound thread; an unmapped owner
//   DM adopts its legacy thread when that thread already exists, and is
//   otherwise given a thread minted by chat.create_thread at the internal
//   tier; either way the choice is bound in the durable map before anything
//   is appended. Outbound, IsBridgeThread is a map lookup and nothing else.
//
// Inputs: the update's Message; the bridge's ThreadMap, LegacyThreadLookup
//   and ChatService seams (BridgeSeams).
//
// Outputs: the thread an inbound message is appended to, or a typed
//   refusal recorded in RefusalReport.
//
// Constraints:
//   - FAIL CLOSED. Any map, lookup or create_thread error refuses the
//     message: no append, no reply, and never a fall back to a derived id.
//   - OWNER DM BEFORE MINTING. A message that is not the owner's direct
//     chat is refused before create_thread runs, so a group never leaves an
//     orphan thread behind; ThreadMap.Bind enforces the same rule (and the
//     paired-sender check) again at the write.
//   - ADOPTED LEGACY THREADS STAY AS THEY ARE. Adoption binds the existing
//     thread and writes no privacy marker, so an unmarked legacy thread
//     keeps reading restricted and its mirroring stays refused.
//   - A LOST RACE COSTS ONE EMPTY THREAD. Two first messages may both mint;
//     Bind returns the winner's thread to both, and the loser's minted
//     thread is never appended to.
//
// SPORT: plugins/cascade-pa/telegram TelegramBridge (thread resolution).

package telegram

import (
	"context"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// bridgeThreadTier is the privacy mode every bridge thread is minted with:
// internal, the narrowest tier the bridge's egress class admits.
const bridgeThreadTier = "internal"

// reasonNoBridgeThread is the RefusalReport text for an inbound message no
// thread could be resolved for. Fixed and content-free.
const reasonNoBridgeThread = "chat has no bridge thread; message not recorded"

// BridgeSeams are TelegramBridge's host-mediated collaborators. Every nil
// field resolves to its fail-closed default.
type BridgeSeams struct {
	Chat       ChatService
	Privacy    ThreadPrivacyResolver
	Divergence DivergenceSink
	Threads    ThreadMap
	Legacy     LegacyThreadLookup
}

// resolveInboundThread returns the thread m is appended to (this file's
// header gives the rules).
func (b *TelegramBridge) resolveInboundThread(ctx context.Context, m *Message) (string, error) {
	if m.Chat.Type != "private" || m.Chat.ID != m.From.ID {
		return "", errBindNotOwnerDM
	}
	threadID, ok, err := b.threads.ThreadFor(ctx, m.Chat.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		if threadID, err = b.candidateThread(ctx, m.Chat.ID); err != nil {
			return "", err
		}
	}
	// Bind runs for a mapped chat too: it re-checks the owner rule on every
	// message and re-asserts a binding whose second row a crash interrupted.
	return b.threads.Bind(ctx, m.Chat, m.From.ID, threadID)
}

// candidateThread is an unmapped chat's thread: its legacy thread when that
// one exists, otherwise a freshly minted internal thread.
func (b *TelegramBridge) candidateThread(ctx context.Context, chatID int64) (string, error) {
	legacy := threadIDForChat(chatID)
	exists, err := b.legacy.ThreadExists(ctx, legacy)
	if err != nil {
		return "", err
	}
	if exists {
		return legacy, nil
	}
	return b.chat.CreateThread(ctx, bridgeThreadTier)
}

// recordUnmapped records an inbound refusal that never reached a thread.
// There is no thread, so no tier and no divergence event; nothing is sent
// to the chat.
func (b *TelegramBridge) recordUnmapped(corrID string) {
	b.recordRefusal(cascadepa.RefusalRecord{
		Reason: reasonNoBridgeThread, CorrelationID: corrID, At: b.module.now(),
	})
}

// IsBridgeThread reports whether threadID is bound to a chat in this
// bridge's map. It is a map lookup only: a thread the map does not hold is
// not a bridge thread, whatever its id looks like. An error means unknown
// and the caller refuses.
func (b *TelegramBridge) IsBridgeThread(ctx context.Context, threadID string) (bool, error) {
	_, ok, err := b.threads.ChatFor(ctx, threadID)
	if err != nil {
		return false, err
	}
	return ok, nil
}
