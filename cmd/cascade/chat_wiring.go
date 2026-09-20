// Purpose: register the chat.* namespace over a real conversation store
// on cascade.db. PORTABLE — this file carries no build tag.
//
// WHY IT IS PORTABLE, AND WHY THAT IS THE FIX. Nothing here is
// unix-specific: sqlite, the migration ledger, the conversation adapter
// and the RPC registry all compile on every supported platform. The file
// was born as `daemon_unix_chat.go` only because it was written beside
// the other `daemon_unix_*` composition files, and the `!windows` tag it
// inherited from that neighbourhood broke the Windows build outright —
// `plugin_rpc.go` is untagged, calls this function, and Windows CI failed
// with `undefined: wireChatHandlers` (audit F1, R-14.289).
//
// THE PLATFORM CONTRACT IS UNCHANGED BY THIS. Windows is tier-2: it has
// no daemon, no socket and no RPC server (`daemon_windows.go` refuses
// every daemon verb, `DaemonSupported` is false there), so nothing on
// that platform ever calls this function. Making it compile everywhere
// exposes no daemon and promises no surface — it is exactly the treatment
// `RegisterRecallIndexHandler` and `wirePluginAddHandler`, this call's two
// siblings in `plugin_rpc.go`, already have.
//
// WHY THIS FILE EXISTS AT ALL. `internal/conversation` built the domain,
// the store, the SSE mirror and the JSON-RPC adapter, and nothing ever
// called `Adapter.RegisterHandlers`. So `cascade chat "hello"` against a
// running daemon answered `chat.append_turn: method not found` — the
// CLI's real round trip reaching a registry that had never heard of the
// method (R-14.284).
//
// Inputs: the RPC registry, the event bus, the clock, and the path
// provider siting cascade.db.
// Outputs: chat.append_turn, chat.get_thread and chat.list_threads bound
// on the registry.
// Constraints: the mode passed to the adapter is the DAEMON mode, never
// `conversation.ModeEmbedded`. Embedded mode suppresses the SSE mirror,
// and the only caller is a daemon that has a live bus — passing embedded
// here would silently drop the client-local echo every chat client
// depends on. The Windows embedded one-shot path does not run through
// this function; it refuses at the transport, which is the documented
// tier-2 behaviour and not something this file may paper over.
//
// SPORT: cmd/cascade chat wiring (ADD) — P1-E20-W5-S43-T5; platform tag
// removed — P1-E45-W10-S88-T2.
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wireChatHandlers binds the chat.* namespace.
//
// Its own second sqlite connection to the same cascade.db, matching
// registerContextEngineHandlers and wireConductorExpand: the schema is
// already applied by openRuntimeStore, and threading one raw *sql.DB
// through every namespace is a larger change than any one of them owns.
func wireChatHandlers(ctx context.Context, registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus) error {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "daemon: chat: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "daemon: chat: open cascade.db")
	}
	// This namespace applies its OWN schema, on its own connection, the
	// way wireConductorExpand does for evidence. openRuntimeStore also
	// applies it, and the migration ledger makes the second call a no-op
	// — but depending on that would make chat's availability a function
	// of whether some other subsystem ran first, which is an ordering
	// nobody reading this file could see.
	if err := conversation.ApplyConversationSchema(
		ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(paths.DataDir(), "backups")); err != nil {
		_ = db.Close()
		return err
	}
	adapter := conversation.NewAdapter(
		conversation.NewStore(db),
		bus,
		// No substitutor. The egress substitution pass belongs on the
		// path where content LEAVES the machine; the SSE mirror is a
		// local echo to a client on this host's own socket, and
		// sse.go's own contract documents nil as a passthrough rather
		// than as a disabled check. Wiring one here would substitute
		// vault references into the operator's own transcript.
		nil,
		clock,
		// The daemon mode, deliberately not conversation.ModeEmbedded —
		// see this file's header. chatDaemonMode names it so the choice
		// is greppable and testable rather than a bare literal.
		chatDaemonMode,
	)
	adapter.RegisterHandlers(registry)
	return nil
}

// chatDaemonMode is the adapter mode this composition root registers
// under: the daemon mode, whose SSE mirror is live.
//
// Named rather than written as a bare "" so TestP1ChatPlatformBoundary can
// assert it, and so a future edit that reached for
// conversation.ModeEmbedded here has something to collide with.
const chatDaemonMode = ""
