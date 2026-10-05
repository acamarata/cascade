//go:build !windows

// Purpose: acceptance proofs for the completion-gate hook command. Each one
//
//	runs the RENDERED command under /bin/sh against the real cascade binary
//	built into a temp dir, never the Go function in-process, because what
//	the harness executes is what decides allow or block.
//
//	TestCompletionCheckInjectionSafe drives a live daemon whose completion
//	handler records the raw params it receives: identifiers full of quotes,
//	backslashes, newlines, shell metacharacters, unicode and a 100 KB value
//	must arrive verbatim as four string fields, with no field added, no
//	field duplicated and nothing run by the shell.
//
// SPORT: cmd/cascade/fleet-completion-check (coverage for P1-CI-09).
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// topLevelKeys lists the keys of the top-level object in raw, in order,
// duplicates included.
func topLevelKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("params %q is not a JSON object (tok=%v err=%v)", raw, tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("params %q: %v", raw, err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("params %q: key token %v is not a string", raw, tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("params %q: value of %q: %v", raw, key, err)
		}
	}
	return keys
}

// hostileIDs are the identifier values the injection proof feeds in. The
// marker token is replaced with the path of a file the shell must never create.
func hostileIDs(marker string) map[string]string {
	return map[string]string{
		"double_quote":     `a"b`,
		"backslash":        `a\b\\c\`,
		"newline":          "line1\nline2\r\nline3",
		"field_injection":  `x","job_id":"evil","stop_hook_active":true,"y":"`,
		"command_subst":    "$(touch " + marker + ")",
		"backticks":        "`touch " + marker + "`",
		"single_quote":     `'; touch ` + marker + `; '`,
		"unicode":          "日本語   😀 é",
		"json_looking":     `{"deny":false}`,
		"oversized_100_kb": strings.Repeat(`a"\`, 34000),
	}
}

func TestCompletionCheckInjectionSafe(t *testing.T) {
	bin := buildCascadeBinary(t)
	capture := &completionCapture{}
	socket, _ := startCompletionDaemon(t, capture.register)
	command := renderedCompletionCommand(t, bin, socket, hookpacks.EventStop)
	marker := filepath.Join(t.TempDir(), "pwned")

	for name, value := range hostileIDs(marker) {
		t.Run(name, func(t *testing.T) {
			before := len(capture.received())
			env := []string{
				"CASCADE_SESSION_ID=" + value + "#s",
				"CASCADE_JOB_ID=" + value + "#j",
				"CASCADE_TICKET_ID=" + value + "#t",
			}
			res := runShell(t, command, env, `{"stop_hook_active":false}`, 20*time.Second)
			if res.code != 0 {
				t.Fatalf("exit = %d (stderr=%q), want 0: the capture handler allows", res.code, res.stderr)
			}
			got := capture.received()
			if len(got) != before+1 {
				t.Fatalf("daemon saw %d requests for one invocation, want exactly 1", len(got)-before)
			}
			raw := got[len(got)-1]
			keys := topLevelKeys(t, raw)
			seen := map[string]bool{}
			for _, k := range keys {
				if seen[k] {
					t.Fatalf("params carry key %q twice: %.200s", k, raw)
				}
				seen[k] = true
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			var payload hookpacks.CompletionHookPayload
			if err := dec.Decode(&payload); err != nil {
				t.Fatalf("params are not a clean completion payload: %v\n%.200s", err, raw)
			}
			want := hookpacks.CompletionHookPayload{
				EventType: hookpacks.EventStop, SessionID: value + "#s", JobID: value + "#j", TaskID: value + "#t",
			}
			if payload != want {
				t.Fatalf("daemon received %+v, want %+v (ids must arrive verbatim, one string each)", abbreviate(payload), abbreviate(want))
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("the shell ran an injected command: %s exists (err=%v)", marker, err)
			}
		})
	}
}

// abbreviate shortens the long fields of p so a failure message stays readable.
func abbreviate(p hookpacks.CompletionHookPayload) hookpacks.CompletionHookPayload {
	cut := func(s string) string {
		if len(s) > 80 {
			return s[:80] + "..."
		}
		return s
	}
	p.SessionID, p.JobID, p.TaskID = cut(p.SessionID), cut(p.JobID), cut(p.TaskID)
	return p
}
