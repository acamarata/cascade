// Purpose (this file): the P1-PLG-01 fix-2 review cases against the
//
//	handshake: a bare t.TempDir()-based project directory (no dot, 40+
//	chars) is proposed and written as project_dir, and a known-prefix key
//	that `nself config get POSTGRES_HOST` returns hidden by a zero-width
//	rune (ZWSP, ZWNJ, BOM) is withheld by path, never proposed, never in
//	the JSON and never written in APPLY mode.
//
// Constraints: no nself binary is forked (activeRunner is scripted); the
//
//	APPLY legs write through the real runtime ConfigWriter under
//	t.TempDir(); credential-shaped literals are concatenated (C22).
//
// SPORT: plugins/nself handshake (TEST) — P1-PLG-01.

package nself

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// projectDirPath is where the handshake proposes the project directory.
const projectDirPath = "plugins." + handshakeOwner + ".project_dir"

func TestHandshakeProposesBareTempDirProjectDir(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{base, filepath.Join(base, "acamarata", "cascade-fixture", "nself-app")} {
		dir = nselfProjectDirAt(t, dir)
		scriptWithHost(t, "db.internal")
		resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
		if err != nil {
			t.Fatalf("propose(%s): %v", dir, err)
		}
		got, ok := entryLiteral(entriesFromWire(resp.Proposed), projectDirPath)
		if !ok || got != strconv.Quote(dir) || len(resp.Withheld) != 0 {
			t.Fatalf("project_dir = %q (proposed %v), withheld %v; want %q proposed, nothing withheld",
				got, ok, resp.Withheld, dir)
		}
		cfg := filepath.Join(t.TempDir(), "config.toml")
		configApplier = writerApplier{path: cfg}
		if _, err := runHandshake(context.Background(), handshakeModeApply, dir); err != nil {
			t.Fatalf("apply(%s): %v", dir, err)
		}
		data, err := os.ReadFile(cfg)
		if err != nil || !strings.Contains(string(data), strconv.Quote(dir)) {
			t.Fatalf("config.toml (%v) lacks project_dir %q:\n%s", err, dir, data)
		}
	}
}

func TestHandshakeWithholdsZeroWidthSecret(t *testing.T) {
	key := splitKeyHead + splitKeyTail
	for name, host := range map[string]string{
		"zwsp leading":   "\u200b" + key,
		"zwsp in prefix": "s\u200b" + key[1:],
		"zwnj in prefix": "sk\u200c" + key[2:],
		"bom leading":    "\ufeff" + key,
	} {
		t.Run(name, func(t *testing.T) {
			dir := nselfProjectDir(t)
			scriptWithHost(t, host)
			resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
			if err != nil {
				t.Fatalf("propose: %v", err)
			}
			proposed := entriesFromWire(resp.Proposed)
			if lit, ok := entryLiteral(proposed, hostPath); ok {
				t.Fatalf("the zero-width-hidden key was proposed: %s", lit)
			}
			assertNoSplitKey(t, resp)
			if _, ok := entryLiteral(proposed, projectDirPath); !ok {
				t.Fatalf("project_dir not proposed (guard against an empty proposal): %+v", resp)
			}
			if len(resp.Withheld) != 1 || resp.Withheld[0] != hostPath {
				t.Fatalf("withheld = %v, want exactly [%s]", resp.Withheld, hostPath)
			}
			assertApplyWritesNoSplitKey(t, dir)
		})
	}
}
