//go:build !windows

// Purpose: register the chat.* namespace on the daemon's socket, over a
// real conversation store on the daemon's own cascade.db.
//
// WHY THIS FILE EXISTS. `internal/conversation` built the domain, the
// store, the SSE mirror and the JSON-RPC adapter, and nothing ever called
// `Adapter.RegisterHandlers`. So `cascade chat "hello"` against a running
// daemon answered `chat.append_turn: method not found` — the CLI's real
// round trip reaching a registry that had never heard of the method. The
// `chat` noun is reserved in pkg/plugin's R5 blocklist precisely because
// the host owns it; the host was not serving it (R-14.284).
//
// adapter.go's header records the gap and says the call site is "outside
// this ticket's files_scope" and that RegisterHandlers "is ready for that
// call site today". Both were true when written. This is that call site.
//
// Inputs: the daemon's RPC registry, its event bus, its clock, and the
// already-migrated cascade.db path — `openRuntimeStore` applies the
// conversation schema, so the tables are there before this runs.
// Outputs: chat.append_turn, chat.get_thread and chat.list_threads bound
// on the daemon's socket.
// Constraints: mode is the DAEMON mode, not embedded. The adapter refuses
// the SSE mirror under `conversation.ModeEmbedded`, which is the Windows
// tier-2 path; this file is `!windows` and a daemon that has a bus is by
// definition not that path, so passing the embedded mode here would
// silently drop the client-local echo every chat client depends on.
//
// SPORT: cmd/cascade daemon chat wiring (ADD) — P1-E20-W5-S43-T5.
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
		// see this file's header.
		"",
	)
	adapter.RegisterHandlers(registry)
	return nil
}
