// Purpose (this file): the Telegram view of the durable chat-to-thread map
//   (cascadepa.ThreadMapStore): which conversation thread a paired chat
//   writes to, which chat a thread mirrors to, and WHO may create that
//   binding. Every bridge thread comes from here; no thread id is ever
//   derived from a chat id on the inbound or outbound path.
//
// Inputs: the map store, the pairing BindingStore and the bridge subject;
//   per Bind, the update's Chat and its sender id.
//
// Outputs: ThreadMap (ThreadFor / ChatFor / Bind) and the fail-closed
//   defaults for an unwired map and an unwired legacy-thread lookup.
//
// Constraints:
//   - OWNER DM ONLY. Bind accepts only a private chat whose id equals the
//     sender's id, and only when that sender is on the paired binding's
//     allowlist. A group, a channel, a chat.id != from.id or an unpaired
//     sender is KindPolicyDenied and nothing is written.
//   - LEGACY IDS STAY THEIR OWN CHAT'S. A thread id in the legacy
//     "bridge-telegram:<chat>" form binds only to that exact chat, in its
//     canonical form, so one chat can never adopt another chat's history.
//   - FAIL CLOSED. A nil map or lookup resolves to a default whose every
//     call errors; callers refuse on any error and never fall back to a
//     derived id.
//
// SPORT: plugins/cascade-pa/telegram ThreadMap (chat-to-thread map).

package telegram

import (
	"context"
	"strconv"
	"strings"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ThreadMap resolves bridge chats to conversation threads and back.
type ThreadMap interface {
	// ThreadFor returns the thread chatID is bound to.
	ThreadFor(ctx context.Context, chatID int64) (string, bool, error)
	// ChatFor returns the chat threadID is bound to; a thread the map does
	// not hold is not a bridge thread.
	ChatFor(ctx context.Context, threadID string) (int64, bool, error)
	// Bind binds chat to threadID once and returns the bound thread (an
	// existing binding wins and is returned). Only the owner's direct chat
	// may bind; anything else is KindPolicyDenied with nothing written.
	Bind(ctx context.Context, chat Chat, fromID int64, threadID string) (bound string, err error)
}

// LegacyThreadLookup answers whether a conversation thread already exists,
// so a chat whose legacy "bridge-telegram:<chat>" thread holds history is
// bound to it instead of being given a fresh one. The host implements it
// over its own conversation store (this package never imports internal/).
type LegacyThreadLookup interface {
	ThreadExists(ctx context.Context, threadID string) (bool, error)
}

// errBindNotOwnerDM refuses a binding outside the owner's direct chat.
var errBindNotOwnerDM = cascade.New(cascade.KindPolicyDenied,
	"cascade-pa/telegram: only the paired owner's direct chat can be bound to a thread")

// errNoThreadMap / errNoLegacyLookup are the fail-closed refusals of an
// unwired host.
var (
	errNoThreadMap = cascade.New(cascade.KindUnavailable,
		"cascade-pa/telegram: no chat-to-thread map is wired; the bridge refuses every message")
	errNoLegacyLookup = cascade.New(cascade.KindUnavailable,
		"cascade-pa/telegram: no legacy thread lookup is wired; the bridge refuses to map a new chat")
)

// durableThreadMap is ThreadMap over cascadepa.ThreadMapStore.
type durableThreadMap struct {
	store   *cascadepa.ThreadMapStore
	binding *cascadepa.BindingStore
	subject string
}

// NewThreadMap builds the bridge's ThreadMap for subject. A nil store or
// binding store yields the fail-closed default.
func NewThreadMap(store *cascadepa.ThreadMapStore, binding *cascadepa.BindingStore, subject string) ThreadMap {
	if store == nil || binding == nil || subject == "" {
		return unconfiguredThreadMap{}
	}
	return durableThreadMap{store: store, binding: binding, subject: subject}
}

func (m durableThreadMap) ThreadFor(ctx context.Context, chatID int64) (string, bool, error) {
	return m.store.ThreadFor(ctx, m.subject, chatID)
}

func (m durableThreadMap) ChatFor(ctx context.Context, threadID string) (int64, bool, error) {
	return m.store.ChatFor(ctx, m.subject, threadID)
}

func (m durableThreadMap) Bind(ctx context.Context, chat Chat, fromID int64, threadID string) (string, error) {
	if chat.Type != "private" || chat.ID != fromID {
		return "", errBindNotOwnerDM
	}
	allowed, err := m.binding.IsAllowed(ctx, m.subject, strconv.FormatInt(fromID, 10))
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", errBindNotOwnerDM
	}
	if strings.HasPrefix(threadID, bridgeThreadPrefix) {
		if legacyChat, ok := chatIDForThread(threadID); !ok || legacyChat != chat.ID {
			return "", errBindNotOwnerDM
		}
	}
	return m.store.Bind(ctx, m.subject, chat.ID, threadID)
}

// MayBeBridgeThread reports whether threadID could be held by any bridge's
// map. It is a NECESSARY condition only, for a caller outside the daemon
// that cannot read the map: false means "never a bridge thread" (empty, or
// the legacy prefix in a non-canonical form, which Bind never accepts);
// true means "ask the daemon", never "is a bridge thread".
func MayBeBridgeThread(threadID string) bool {
	if threadID == "" {
		return false
	}
	if !strings.HasPrefix(threadID, bridgeThreadPrefix) {
		return true
	}
	_, canonical := chatIDForThread(threadID)
	return canonical
}

// IsLegacyBridgeThreadID reports whether threadID is a canonical legacy
// "bridge-telegram:<chat id>" id. A host with no chat bridge wired uses it
// to tell a thread the retired bridge could have written to from an ordinary
// daemon-minted thread, which is never a bridge thread.
func IsLegacyBridgeThreadID(threadID string) bool {
	if !strings.HasPrefix(threadID, bridgeThreadPrefix) {
		return false
	}
	_, canonical := chatIDForThread(threadID)
	return canonical
}

// unconfiguredThreadMap fails every call: an unwired host maps nothing,
// appends nothing and sends nothing.
type unconfiguredThreadMap struct{}

func (unconfiguredThreadMap) ThreadFor(context.Context, int64) (string, bool, error) {
	return "", false, errNoThreadMap
}

func (unconfiguredThreadMap) ChatFor(context.Context, string) (int64, bool, error) {
	return 0, false, errNoThreadMap
}

func (unconfiguredThreadMap) Bind(context.Context, Chat, int64, string) (string, error) {
	return "", errNoThreadMap
}

func threadMapOrRefuseAll(m ThreadMap) ThreadMap {
	if m == nil {
		return unconfiguredThreadMap{}
	}
	return m
}

// unconfiguredLegacyLookup fails every call, so an unwired host never
// guesses whether a legacy thread exists.
type unconfiguredLegacyLookup struct{}

func (unconfiguredLegacyLookup) ThreadExists(context.Context, string) (bool, error) {
	return false, errNoLegacyLookup
}

func legacyOrRefuseAll(l LegacyThreadLookup) LegacyThreadLookup {
	if l == nil {
		return unconfiguredLegacyLookup{}
	}
	return l
}
