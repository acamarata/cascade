//go:build !windows

// Purpose: the chat turn journal proven through the REAL daemon composition
// root (platformDaemonRun), not wireChatHandlers alone. The unit tests in
// chat_journal_wiring_test.go prove the wiring function; these prove the
// daemon calls it with its runtime store, journals a live chat turn, and
// refuses to start over a journal that cannot resume.
//
// Inputs: a throwaway cascade home per test; the runtime store is opened
// with openRuntimeStore before the daemon starts to seed it, exactly as a
// daemon restarting over an existing cascade.db would find it.
// Outputs: none. Constraints: no fault-injection hook exists or is used;
// seeding goes through conversation.AppendTurnJournaled and the journal's
// public Append, and the daemon is the unmodified production composition.
//
// SPORT: cmd/cascade chat journal wiring tests (ADD) — P1-CPA-08.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// withRuntimeStore opens the daemon's on-disk runtime store under deps,
// runs fn, and closes it, so the daemon can take the database afterwards.
func withRuntimeStore(t *testing.T, deps daemonDeps, fn func(store provider.Store)) {
	t.Helper()
	store, _, closeStore, err := openRuntimeStore(context.Background(), deps.Paths, deps.Clock)
	if err != nil {
		t.Fatalf("open the runtime store: %v", err)
	}
	defer closeStore()
	fn(store)
}

// journalDaemon is a platformDaemonRun in flight.
type journalDaemon struct {
	cancel context.CancelFunc
	done   chan error
}

// startJournalDaemon runs platformDaemonRun and returns once its socket
// answers. It fails the test if the run exits first.
func startJournalDaemon(t *testing.T, deps daemonDeps) *journalDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &journalDaemon{cancel: cancel, done: make(chan error, 1)}
	go func() { d.done <- platformDaemonRun(ctx, deps) }()
	t.Cleanup(func() { d.cancel() })
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-d.done:
			t.Fatalf("platformDaemonRun exited before serving: %v", err)
		default:
		}
		if socketDialable(deps.Paths.SocketPath()) {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("platformDaemonRun never served its socket")
	return nil
}

// stop cancels the run and requires a clean return.
func (d *journalDaemon) stop(t *testing.T) {
	t.Helper()
	d.cancel()
	select {
	case err := <-d.done:
		if err != nil {
			t.Fatalf("platformDaemonRun after cancel: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("platformDaemonRun did not return after its context was cancelled")
	}
}

// unixRPCClient is the production daemon client (internal/client) over the
// daemon's unix socket: the same path `cascade chat` takes.
func unixRPCClient(sock string) *client.Client {
	return client.New(sock, client.UnixDialer, 15*time.Second)
}

// journalRPC calls one daemon method and returns its result, failing the
// test on any error response.
func journalRPC(t *testing.T, c *client.Client, method string, params map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := c.Do(context.Background(), method, params, &out); err != nil {
		t.Fatalf("%s over the daemon socket: %v", method, err)
	}
	return out
}

// ackedChatEntries returns thread's ACKed entries in the chat journal of the
// runtime store under deps (the daemon must not be running).
func ackedChatEntries(t *testing.T, deps daemonDeps, thread string) []journal.Entry {
	t.Helper()
	var entries []journal.Entry
	withRuntimeStore(t, deps, func(store provider.Store) {
		js := journal.New(store, deps.Clock, chatJournalNamespace)
		got, err := js.Replay(context.Background(), thread, journal.Cursor{}, []journal.Kind{journal.KindAck})
		if err != nil {
			t.Fatalf("replay the chat journal: %v", err)
		}
		entries = got
	})
	return entries
}

// TestPlatformDaemonRunJournalsChat starts the production daemon, appends a
// turn over its socket, stops it, and requires the ACKed journal entry in the
// runtime store: the daemon passed its store to the chat wiring.
func TestPlatformDaemonRunJournalsChat(t *testing.T) {
	deps := newRunTestDeps(t, nil)
	d := startJournalDaemon(t, deps)
	res := journalRPC(t, unixRPCClient(deps.Paths.SocketPath()), "chat.append_turn", map[string]any{
		"thread_id": "th-daemon", "role": "user",
		"segments": []map[string]string{{"kind": "text", "content": "journaled by the daemon"}},
	})
	if id, _ := res["turn_id"].(string); id == "" {
		t.Fatalf("chat.append_turn returned no turn id: %v", res)
	}
	d.stop(t)

	if n := len(ackedChatEntries(t, deps, "th-daemon")); n != 1 {
		t.Fatalf("runtime store holds %d ACKed chat journal entries for the thread, want 1", n)
	}
}

// TestPlatformDaemonRunRefusesCorruptChatJournal seeds an ACKed entry that
// cannot decode and requires platformDaemonRun to return without serving:
// KindIntegrity, the wiring entry named, the thread named.
func TestPlatformDaemonRunRefusesCorruptChatJournal(t *testing.T) {
	deps := newRunTestDeps(t, nil)
	withRuntimeStore(t, deps, func(store provider.Store) {
		seedUndecodableChatEntry(t, store, deps.Clock, "th-corrupt")
	})

	err := platformDaemonRun(context.Background(), deps)
	if err == nil {
		t.Fatal("platformDaemonRun started over a chat journal that cannot resume")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("start refusal kind = %v (ok=%v), want KindIntegrity: %v", kind, ok, err)
	}
	for _, want := range []string{`daemon wiring "`, "chat: resume journaled thread th-corrupt"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("start refusal %q does not contain %q", err.Error(), want)
		}
	}
	if socketDialable(deps.Paths.SocketPath()) {
		t.Fatal("the daemon is serving despite the refused start")
	}
}
