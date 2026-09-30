// Purpose (this file): the adversarial-review case "a secret split across
//
//	whitespace" against the handshake: a known-prefix key that `nself
//	config get POSTGRES_HOST` returns cut by a space, a tab or several
//	spaces is withheld (named by path only), never proposed, never echoed
//	and never written in APPLY mode, while a plain host is still proposed.
//
// Constraints: no nself binary is forked (activeRunner is scripted); the
//
//	APPLY leg writes through the real runtime ConfigWriter under
//	t.TempDir(); credential-shaped literals are concatenated (C22).
//
// SPORT: plugins/nself handshake (TEST) — P1-PLG-01.

package nself

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// splitKeyHead/splitKeyTail are the halves of a known-prefix key.
const (
	splitKeyHead = "sk-" + "live-" + "abcdefghijkl"
	splitKeyTail = "mnopqrstuvwx"
)

// hostPath is where the handshake proposes POSTGRES_HOST.
const hostPath = "plugins." + handshakeOwner + ".postgres_host"

// scriptWithHost is fullScript with POSTGRES_HOST answering host.
func scriptWithHost(t *testing.T, host string) {
	t.Helper()
	script := fullScript()
	script.out["config get POSTGRES_HOST"] = []byte(host)
	origRunner, origApplier := activeRunner, configApplier
	activeRunner = script
	t.Cleanup(func() { activeRunner, configApplier = origRunner, origApplier })
	withGetenv(t, true, "")
}

func TestHandshakeWithholdsSplitSecret(t *testing.T) {
	for name, host := range map[string]string{
		"one space":      splitKeyHead + " " + splitKeyTail,
		"tab":            splitKeyHead + "\t" + splitKeyTail,
		"several spaces": splitKeyHead + "    " + splitKeyTail,
	} {
		t.Run(name, func(t *testing.T) {
			dir := nselfProjectDir(t)
			scriptWithHost(t, host)
			resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
			if err != nil {
				t.Fatalf("propose: %v", err)
			}
			proposed := entriesFromWire(resp.Proposed)
			if _, ok := entryLiteral(proposed, "plugins."+handshakeOwner+".project_dir"); !ok {
				t.Fatalf("project_dir not proposed (guard against an empty proposal): %+v", resp)
			}
			if lit, ok := entryLiteral(proposed, hostPath); ok {
				t.Fatalf("the split secret was proposed: %s", lit)
			}
			if len(resp.Withheld) != 1 || resp.Withheld[0] != hostPath {
				t.Fatalf("withheld = %v, want exactly [%s]", resp.Withheld, hostPath)
			}
			assertNoSplitKey(t, resp)
			assertApplyWritesNoSplitKey(t, dir)
		})
	}
}

// assertNoSplitKey fails when either half of the key reaches the encoded
// response.
func assertNoSplitKey(t *testing.T, resp handshakeResponse) {
	t.Helper()
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 || !strings.Contains(string(data), hostPath) {
		t.Fatalf("response does not name the withheld path: %s", data)
	}
	for _, half := range []string{"abcdefghijkl", splitKeyTail} {
		if strings.Contains(string(data), half) {
			t.Fatalf("response leaks %q: %s", half, data)
		}
	}
}

// assertApplyWritesNoSplitKey runs APPLY mode over the real writer and
// fails unless the other entries were written and the key was not.
func assertApplyWritesNoSplitKey(t *testing.T, dir string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	configApplier = writerApplier{path: cfg}
	resp, err := runHandshake(context.Background(), handshakeModeApply, dir)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(resp.Applied) == 0 || len(resp.Withheld) != 1 || resp.Withheld[0] != hostPath {
		t.Fatalf("apply resp = %+v, want applied entries and the host withheld", resp)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "project_dir") {
		t.Fatalf("config.toml lacks project_dir (guard against an empty write):\n%s", data)
	}
	for _, leak := range []string{"postgres_host", "abcdefghijkl", splitKeyTail} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("config.toml carries %q:\n%s", leak, data)
		}
	}
}

// TestHandshakeProposesPlainHost is the no-false-positive leg: an ordinary
// host name is proposed verbatim and nothing is withheld.
func TestHandshakeProposesPlainHost(t *testing.T) {
	dir := nselfProjectDir(t)
	scriptWithHost(t, "db.internal")
	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if lit, ok := entryLiteral(entriesFromWire(resp.Proposed), hostPath); !ok || lit != `"db.internal"` {
		t.Fatalf("postgres_host = %q (proposed %v), want \"db.internal\"", lit, ok)
	}
	if len(resp.Withheld) != 0 {
		t.Fatalf("withheld = %v, want none", resp.Withheld)
	}
}
