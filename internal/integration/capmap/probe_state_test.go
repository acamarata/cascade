//go:build capmap

package capmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	register("TestCapmap_MemoryRemember", probeMemoryRemember)
	register("TestCapmap_SecretsVaultSet", probeVaultSet)
	register("TestCapmap_ConversationJournalAppend", probeJournalAppend)
}

// grepTree reports whether any regular file under root contains needle.
func grepTree(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil //nolint:nilerr // unreadable entries (sockets, locks) are not evidence
		}
		if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// probeMemoryRemember proves `memory remember` stores through the daemon.
// Authorization: a browser-shaped memory.remember is refused and stores
// nothing. Routing: the daemon (no daemonless warning) answered. Side
// effect: after the daemon is stopped, a separate daemonless process reads
// the record from disk and the text is in a file under the data tree.
// Result: the printed address is the address the list shows.
func probeMemoryRemember(t *testing.T) {
	const fact = "capmap fact omega"
	c := newCLI(t)
	sock := c.startDaemon().Daemon.SocketPath
	mustRefuseBrowser(t, sock, "memory.remember", map[string]any{"content": fact})
	if grepTree(t, c.home, fact) {
		t.Fatal("a refused memory.remember left the text on disk")
	}
	r := c.mustOK("", "memory", "remember", fact)
	addr := strings.TrimSpace(r.stdout)
	if addr == "" || strings.Contains(r.stderr, "daemonless") {
		t.Fatalf("remember stdout %q stderr %q, want an address from the daemon", r.stdout, r.stderr)
	}
	c.stopDaemon()
	list := c.mustOK("", "memory", "list")
	if !strings.Contains(list.stdout, addr) || !strings.Contains(list.stdout, fact) {
		t.Fatalf("daemonless list after restart = %q, want %s and %q", list.stdout, addr, fact)
	}
	if !grepTree(t, c.home, fact) {
		t.Fatal("the remembered text is in no file under the isolated home")
	}
}

// probeVaultSet proves `vault set` writes an encrypted custody entry.
// Authorization: an unelevated `vault get` is refused with the typed
// elevation-required error and prints nothing secret. Routing: the vault
// reports the file-vault backend (a real keychain would fail the probe
// before any write). Side effect: vault.age exists and no file under the
// home holds the plaintext. Result: set reports the name and list shows it.
func probeVaultSet(t *testing.T) {
	const secret = "capmap-secret-value-7731"
	c := newCLI(t)
	before := c.mustOK("", "vault", "list")
	if !strings.Contains(before.stdout, "backend: file-vault") {
		t.Fatalf("vault backend is not the file vault, refusing to write: %q", before.stdout)
	}
	set := c.mustOK(secret, "vault", "set", "probe1")
	if !strings.Contains(set.stdout, "stored probe1") {
		t.Fatalf("vault set stdout = %q, want a stored receipt", set.stdout)
	}
	list := c.mustOK("", "vault", "list")
	if !strings.Contains(list.stdout, "probe1") {
		t.Fatalf("vault list = %q, want probe1", list.stdout)
	}
	get := c.run("", "vault", "get", "probe1", "--json")
	if e := decodeEnvelope(t, get); e.OK || e.Error == nil || e.Error.Kind != "elevation-required" {
		t.Fatalf("vault get = %+v, want a typed elevation-required refusal", e)
	}
	if strings.Contains(get.stdout+get.stderr, secret) {
		t.Fatal("the refused get printed the secret")
	}
	if _, err := os.Stat(filepath.Join(c.home, ".cascade", "data", "vault.age")); err != nil {
		t.Fatalf("no vault file on disk: %v", err)
	}
	if grepTree(t, c.home, secret) {
		t.Fatal("the plaintext secret is stored in a file under the home")
	}
}

// probeJournalAppend proves chat.append_turn journals a turn durably.
// Authorization: a browser-shaped append is refused and creates no thread.
// Routing: the owner call lands in chat.get_thread for the same thread.
// Side effect: after a daemon restart the thread and the turn text are
// still there. Result: the append returns a thread id and a turn id.
func probeJournalAppend(t *testing.T) {
	const text = "journal probe turn"
	c := newCLI(t)
	sock := c.startDaemon().Daemon.SocketPath
	params := map[string]any{"role": "user", "segments": []map[string]string{{"kind": "text", "content": text}}}
	mustRefuseBrowser(t, sock, "chat.append_turn", params)
	if n := threadCount(t, sock); n != 0 {
		t.Fatalf("%d thread(s) after a refused append, want 0", n)
	}
	var got struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
	}
	if err := json.Unmarshal(mustRPC(t, sock, "chat.append_turn", params), &got); err != nil || got.ThreadID == "" || got.TurnID == "" {
		t.Fatalf("append result %+v err %v, want a thread id and a turn id", got, err)
	}
	c.stopDaemon()
	sock = c.startDaemon().Daemon.SocketPath
	thread := mustRPC(t, sock, "chat.get_thread", map[string]string{"thread_id": got.ThreadID})
	if !strings.Contains(string(thread), text) || !strings.Contains(string(thread), got.TurnID) {
		t.Fatalf("thread after restart = %s, want the turn %s with %q", thread, got.TurnID, text)
	}
}

// threadCount returns how many chat threads the daemon lists.
func threadCount(t *testing.T, sock string) int {
	t.Helper()
	var out struct {
		Threads []json.RawMessage `json:"threads"`
	}
	if err := json.Unmarshal(mustRPC(t, sock, "chat.list_threads", map[string]any{}), &out); err != nil {
		t.Fatal(err)
	}
	return len(out.Threads)
}
