// Purpose (this file): the P1-PLG-01 fix-3 review cases against the
//
//	handshake: POSTGRES_HOST shaped as "/" plus a 40-char opaque run is
//	withheld (the path exemption covers path-typed keys only), and a
//	known-prefix key hidden by a Default_Ignorable rune outside Cf
//	(U+FE0F, U+034F, U+3164) is withheld; neither is proposed, echoed or
//	written, while the bare t.TempDir() project_dir is still proposed and
//	written.
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
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// keytypeOpaqueHead is the first half of a 40-char opaque run; its text
// must never reach the response or the file.
const keytypeOpaqueHead = "q7Zp2Lx9Rt" + "q7Zp2Lx9Rt"

func TestHandshakeWithholdsOpaqueAbsHost(t *testing.T) {
	dir := nselfProjectDir(t)
	scriptWithHost(t, "/"+keytypeOpaqueHead+"Wm1Hy5Gs0J"+"Wm1Hy5Gs0J")
	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	proposed := entriesFromWire(resp.Proposed)
	if lit, ok := entryLiteral(proposed, hostPath); ok {
		t.Fatalf("the opaque host was proposed: %s", lit)
	}
	if got, ok := entryLiteral(proposed, projectDirPath); !ok || got != strconv.Quote(dir) {
		t.Fatalf("project_dir = %q (proposed %v), want %q", got, ok, dir)
	}
	if len(resp.Withheld) != 1 || resp.Withheld[0] != hostPath {
		t.Fatalf("withheld = %v, want exactly [%s]", resp.Withheld, hostPath)
	}
	data, err := json.Marshal(resp)
	if err != nil || strings.Contains(string(data), keytypeOpaqueHead) {
		t.Fatalf("response (%v) leaks the opaque run: %s", err, data)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	configApplier = writerApplier{path: cfg}
	if _, err := runHandshake(context.Background(), handshakeModeApply, dir); err != nil {
		t.Fatalf("apply: %v", err)
	}
	file, err := os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(file), strconv.Quote(dir)) {
		t.Fatalf("config.toml (%v) lacks project_dir %q:\n%s", err, dir, file)
	}
	for _, leak := range []string{"postgres_host", keytypeOpaqueHead} {
		if strings.Contains(string(file), leak) {
			t.Fatalf("config.toml carries %q:\n%s", leak, file)
		}
	}
}

func TestHandshakeWithholdsDefaultIgnorableSecret(t *testing.T) {
	key := splitKeyHead + splitKeyTail
	for name, host := range map[string]string{
		"variation selector": "s\ufe0f" + key[1:],
		"cgj":                "sk\u034f" + key[2:],
		"hangul filler":      "s\u3164" + key[1:],
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
				t.Fatalf("the hidden key was proposed: %s", lit)
			}
			if _, ok := entryLiteral(proposed, projectDirPath); !ok {
				t.Fatalf("project_dir not proposed (guard against an empty proposal): %+v", resp)
			}
			if len(resp.Withheld) != 1 || resp.Withheld[0] != hostPath {
				t.Fatalf("withheld = %v, want exactly [%s]", resp.Withheld, hostPath)
			}
			assertNoSplitKey(t, resp)
			assertApplyWritesNoSplitKey(t, dir)
		})
	}
}
