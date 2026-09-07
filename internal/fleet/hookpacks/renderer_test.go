// Purpose: HookPack.Render's socket-path substitution, the empty-socket
//
//	refusal, structural parseability of the rendered configuration entry
//	(this ticket's CONTRACT DEVIATION on the missing settings-file
//	fixture, see renderer.go's header), and the daemon-absent-is-fast-
//	and-non-fatal property of the rendered command itself.
//
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func TestRenderer_SubstitutesSocketPathForEveryDescriptor(t *testing.T) {
	pack := hookpacks.SessionsPack()
	rendered, err := pack.Render("/tmp/cascade-test.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(rendered) != len(pack.Descriptors) {
		t.Fatalf("Render returned %d entries, want %d", len(rendered), len(pack.Descriptors))
	}
	for i, raw := range rendered {
		var entry struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("entry %d does not parse: %v", i, err)
		}
		if !strings.Contains(entry.Hooks[0].Command, "/tmp/cascade-test.sock") {
			t.Fatalf("entry %d command = %q, want the socket path substituted in", i, entry.Hooks[0].Command)
		}
		if strings.Contains(entry.Hooks[0].Command, "{{CASCADE_SOCKET_PATH}}") {
			t.Fatalf("entry %d command = %q, still carries the unrendered placeholder", i, entry.Hooks[0].Command)
		}
		if entry.Hooks[0].Type != "command" {
			t.Fatalf("entry %d hook type = %q, want %q", i, entry.Hooks[0].Type, "command")
		}
	}
}

func TestRenderer_EmptySocketPathRefused(t *testing.T) {
	pack := hookpacks.SessionsPack()
	if _, err := pack.Render(""); err == nil {
		t.Fatal("Render(\"\") should refuse with ErrEmptySocketPath")
	}
}

// TestRenderer_CommandCarriesBoundedTimeoutAndNeverFails asserts,
// structurally (no subprocess needed, always runs), that every rendered
// command carries a bounded curl timeout and an unconditional success
// suffix — the two properties that make it non-blocking and non-fatal
// to its host regardless of whether a real daemon is reachable.
func TestRenderer_CommandCarriesBoundedTimeoutAndNeverFails(t *testing.T) {
	pack := hookpacks.SessionsPack()
	rendered, err := pack.Render("/tmp/cascade-test.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for i, raw := range rendered {
		var entry struct {
			Hooks []struct{ Command string } `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		cmd := entry.Hooks[0].Command
		if !strings.Contains(cmd, "-m 1") {
			t.Fatalf("entry %d command = %q, want a bounded curl -m timeout", i, cmd)
		}
		if !strings.HasSuffix(strings.TrimSpace(cmd), "|| true") {
			t.Fatalf("entry %d command = %q, want an unconditional \"|| true\" success suffix", i, cmd)
		}
	}
}

// TestRenderer_DaemonAbsentFastAndNonFatal actually runs one rendered
// command against a socket path that names no live listener and proves
// it returns quickly (well under curl's own 1s bound) with exit 0 —
// the property this ticket's non-negotiables call the one that matters
// most, since "no daemon running" is the common case in the field. It
// skips cleanly when curl is unavailable rather than failing the whole
// package on an environment gap.
func TestRenderer_DaemonAbsentFastAndNonFatal(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available in this environment")
	}
	pack := hookpacks.SessionsPack()
	rendered, err := pack.Render("/tmp/cascade-hookpacks-no-such-socket.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var entry struct {
		Hooks []struct{ Command string } `json:"hooks"`
	}
	if err := json.Unmarshal(rendered[0], &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", entry.Hooks[0].Command)
	runErr := cmd.Run()
	elapsed := time.Since(start)

	if ctx.Err() != nil {
		t.Fatalf("rendered command did not return within the test's bounded timeout: %v", ctx.Err())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("rendered command took %s against an absent daemon, want well under curl's own 1s bound", elapsed)
	}
	if runErr != nil {
		t.Fatalf("rendered command exited non-zero (%v) with no daemon present; it must never fail its host", runErr)
	}
}
