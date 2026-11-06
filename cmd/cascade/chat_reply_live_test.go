//go:build live && !windows

package main

// Purpose: P1-CPA-13d's live proof. A real daemon, built from this tree and
//   run under a throwaway HOME, USERPROFILE and CASCADE_HOME with the file
//   vault forced, gets one gemini lane from `cascade provider add --key-env`
//   and answers one user turn through chat.reply_open and chat.reply. The
//   redacted exchange and its provenance are written to
//   testdata/chat_reply/ (recorded_exchange.json, README.md).
// Constraints: without CASCADE_LIVE_PROVIDER_SMOKE=1 the test skips and
//   names the flag (CI keeps the skip). With it set, a missing
//   GEMINI_FREE_KEY_1 FAILS, as does any failed or empty exchange. The key
//   reaches only the `provider add` child, by --key-env; it is never
//   printed, logged, passed to the daemon or written to the recording, and
//   the recording is refused if it carries the key.
//   The recording is the one write outside t.TempDir(): it is this test's
//   output, made only on the explicit live run.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	liveSmokeVar      = "CASCADE_LIVE_PROVIDER_SMOKE"
	liveReplyLane     = "gemini-chat"
	liveReplyPrompt   = "Reply with one short sentence: what is the capital of France?"
	chatReplyFixtures = "testdata/chat_reply"
)

// recordedExchange is the committed, redacted shape of one live exchange.
type recordedExchange struct {
	Provider      string   `json:"provider"`
	LaneKind      string   `json:"lane_kind"`
	PrivacyMode   string   `json:"privacy_mode"`
	Methods       []string `json:"methods"`
	Prompt        string   `json:"prompt"`
	Reply         string   `json:"reply"`
	Replayed      bool     `json:"replayed"`
	AssistantTurn bool     `json:"assistant_turn_persisted"`
	CapturedAt    string   `json:"captured_at"`
	Redactions    string   `json:"redactions"`
}

// requireReplySmoke skips without the smoke flag and fails without the key.
func requireReplySmoke(t *testing.T) string {
	t.Helper()
	if os.Getenv(liveSmokeVar) != "1" {
		t.Skip(liveSmokeVar + " is not 1: this live proof needs a real gemini lane and the session key")
	}
	key := os.Getenv(liveKeyVar)
	if key == "" {
		t.Fatalf("%s is not exported: %s=1 needs the build session's key", liveKeyVar, liveSmokeVar)
	}
	return key
}

// addReplyLane adds the one gemini lane; the key travels only by --key-env.
func addReplyLane(t *testing.T, r *liveRig, key string) {
	t.Helper()
	out, err := r.run(t, []string{liveKeyVar + "=" + key}, "provider", "add", liveReplyLane,
		"--key-env", liveKeyVar, "--kind", "gemini")
	if err != nil {
		t.Fatalf("provider add %s: %v\n%s", liveReplyLane, err, strings.ReplaceAll(out, key, "[redacted]"))
	}
}

// liveReplyExchange runs one user turn through chat.reply on the daemon.
func liveReplyExchange(t *testing.T, r *liveRig) recordedExchange {
	t.Helper()
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	r.rpc(t, "chat.create_thread", map[string]string{"privacy_mode": "internal"}, &created)
	var turn struct {
		TurnID string `json:"turn_id"`
	}
	r.rpc(t, "chat.append_turn", map[string]any{"thread_id": created.ThreadID, "role": "user",
		"segments": []map[string]string{{"kind": "text", "content": liveReplyPrompt}}}, &turn)
	var opened struct {
		StreamID string `json:"stream_id"`
	}
	r.rpc(t, "chat.reply_open", map[string]string{"thread_id": created.ThreadID, "turn_id": turn.TurnID}, &opened)
	if opened.StreamID == "" {
		t.Fatal("chat.reply_open returned no stream_id")
	}
	var res struct {
		AssistantTurnID string `json:"assistant_turn_id"`
		Replayed        bool   `json:"replayed"`
	}
	r.rpc(t, "chat.reply", map[string]string{"stream_id": opened.StreamID}, &res)
	if res.AssistantTurnID == "" {
		t.Fatal("chat.reply returned no assistant_turn_id")
	}
	reply := assistantText(t, r, created.ThreadID, res.AssistantTurnID)
	if strings.TrimSpace(reply) == "" {
		t.Fatal("the persisted assistant turn carries no text: the exchange failed")
	}
	return recordedExchange{
		Provider: "providers/gemini", LaneKind: "gemini", PrivacyMode: "internal",
		Methods: []string{"chat.create_thread", "chat.append_turn", "chat.reply_open", "chat.reply", "chat.get_thread"},
		Prompt:  liveReplyPrompt, Reply: reply, Replayed: res.Replayed, AssistantTurn: true,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Redactions: "thread, turn and stream ids, the lane key and every header are omitted",
	}
}

// assistantText returns the text of the persisted assistant turn.
func assistantText(t *testing.T, r *liveRig, threadID, assistantID string) string {
	t.Helper()
	var got struct {
		Turns []struct {
			Turn struct {
				ID   string `json:"id"`
				Role string `json:"role"`
			} `json:"turn"`
			Segments []struct {
				Content string `json:"content"`
			} `json:"segments"`
		} `json:"turns"`
	}
	r.rpc(t, "chat.get_thread", map[string]string{"thread_id": threadID}, &got)
	for _, tw := range got.Turns {
		if tw.Turn.ID != assistantID || tw.Turn.Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, s := range tw.Segments {
			b.WriteString(s.Content)
		}
		return b.String()
	}
	t.Fatalf("chat.get_thread holds no assistant turn %s", assistantID)
	return ""
}

// writeRecording writes the redacted exchange and its provenance README,
// refusing any byte sequence that carries the key.
func writeRecording(t *testing.T, ex recordedExchange, key string) {
	t.Helper()
	body, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	readme := provenanceREADME(ex)
	if bytes.Contains(body, []byte(key)) || strings.Contains(readme, key) {
		t.Fatal("the recording carries the lane key: refused, nothing written")
	}
	if err := os.MkdirAll(chatReplyFixtures, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chatReplyFixtures, "recorded_exchange.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chatReplyFixtures, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
}

// provenanceREADME renders the provenance record for the recording.
func provenanceREADME(ex recordedExchange) string {
	return fmt.Sprintf(`# chat_reply fixture provenance

`+"`recorded_exchange.json`"+` is one live chat exchange: a real daemon built from
this tree answered one user turn through chat.reply_open and chat.reply on a
providers/gemini lane, and the persisted assistant turn was read back with
chat.get_thread.

| Field | Value |
|---|---|
| Provider | %s (lane kind %s, added by `+"`cascade provider add --key-env`"+`) |
| Thread privacy | %s |
| RPC methods | %s |
| Captured | %s |
| Capture command | `+"`env %s=1 go test -count=1 -tags=live -run '^(TestP1ChatReplyLive)$' ./cmd/cascade/`"+` |
| Custody | throwaway HOME, USERPROFILE and CASCADE_HOME in t.TempDir(); file vault forced |
| Redactions | %s |
| Credential / personal data | none: the key reached only the provider add child and the recording is refused if it carries it; the prompt is a fixed public question |

To refresh it, export %s, run the capture command, and commit both files.
`, ex.Provider, ex.LaneKind, ex.PrivacyMode, strings.Join(ex.Methods, ", "), ex.CapturedAt,
		liveSmokeVar, ex.Redactions, liveKeyVar)
}

func TestP1ChatReplyLive(t *testing.T) {
	key := requireReplySmoke(t)
	r := newLiveRig(t)
	addReplyLane(t, r, key)
	r.start(t)
	ex := liveReplyExchange(t, r)
	writeRecording(t, ex, key)
}
