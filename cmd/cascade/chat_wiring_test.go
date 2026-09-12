// Purpose: proves `cascade chat`, executed through the REAL root command
//
//	tree (newRootCmd, the same tree main.go builds), reaches a genuine
//	internal/client-backed adapter and never
//	plugins/cascade-pa/cmd.unconfiguredClient's canned "no daemon adapter
//	is wired" message -- the specific gap
//	internal/build/testonly-allow.json recorded for
//	plugins/cascade-pa/cmd.SetClient, closed by
//	internal/plugins/cascadepa_wiring.go and cmd/cascade/builtin_plugins.go's
//	mountChatCmd. Calling pacmd.SetClient directly (as chat_test.go's own
//	package-boundary tests already do) proves nothing about this
//	composition root -- these tests never call it, only execRoot.
//
// Constraints: CASCADE_HOME is a short os.MkdirTemp() dir (Art.7.1's
//
//	sockaddr_un length limit, matching root_daemon_run_test.go's
//	shortCascadeHome), never t.TempDir(). No daemon is started -- the
//	socket path genuinely has nothing listening, so the real client's own
//	transport-classified error is what proves the wiring, without a live
//	round trip.
//
// SPORT: cmd/cascade:chat-mount (TEST) -- FIX-cascade-chat-client-wiring.
package main

import (
	"strings"
	"testing"
)

// chatUnconfiguredMessage is the exact substring
// plugins/cascade-pa/cmd.errClientUnconfigured carries. A wired real
// client must never produce this string.
const chatUnconfiguredMessage = "no daemon adapter is wired into this binary"

// chatRealClientMessage is the substring internal/client's own
// classifyTransportError produces for an unreachable unix socket
// (client.go's "client: <method> daemon not running or unreachable at
// <path>"). Only a genuine internal/client.Client dial produces this.
const chatRealClientMessage = "daemon not running or unreachable at"

func TestChatReachesRealClientAdapter(t *testing.T) {
	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)

	_, err := execRoot(t, "chat", "hello")
	if err == nil {
		t.Fatal("execRoot(chat, hello): err = nil, want a daemon-unreachable error")
	}
	got := err.Error()
	if !strings.Contains(got, chatRealClientMessage) {
		t.Errorf("chat error = %q, want it to contain %q (internal/client's own classified error)", got, chatRealClientMessage)
	}
	if strings.Contains(got, chatUnconfiguredMessage) {
		t.Errorf("chat error = %q, contains unconfiguredClient's canned message -- the composition root did not wire a real client", got)
	}
}

// TestChatCommandMountedOnRealRoot proves `chat` is a direct child of the
// real root command tree (not merely constructible via
// plugins/cascade-pa/cmd.NewChatCommand in isolation, which chat_test.go's
// own package-boundary tests already cover).
func TestChatCommandMountedOnRealRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"chat"})
	if err != nil {
		t.Fatalf("root.Find([chat]): %v", err)
	}
	if cmd.Name() != "chat" {
		t.Fatalf("root.Find([chat]) resolved to %q, want \"chat\"", cmd.Name())
	}
}
