// Purpose: prove the chat command in the built binary reaches the real
// daemon-backed client rather than the unconfigured stand-in, so the reply
// docs describe behaviour the production wiring delivers.
// PORTABLE: no build tag; no network import.
package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestP1ChatReplyDocsGate runs `chat -m` through the production root command
// against a home with no daemon. The real client dials the daemon socket and
// fails there; the stand-in would name its missing wiring instead.
func TestP1ChatReplyDocsGate(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"chat", "-m", "hello"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("chat -m with no daemon must fail, got output %q", out.String())
	}
	if strings.Contains(err.Error(), "no daemon adapter is wired") {
		t.Fatalf("chat still uses the unconfigured client: %v", err)
	}
	if _, statErr := os.Stat(os.Getenv("CASCADE_HOME")); statErr != nil {
		t.Fatalf("home vanished: %v", statErr)
	}
}
