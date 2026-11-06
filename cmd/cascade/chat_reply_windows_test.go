//go:build windows

// Purpose: on Windows (tier-2, no daemon) `cascade chat -m` has no daemon to
//
//	reach for chat.reply, so the daemon it requires refuses with the typed
//	tier-2 error (the chat.reply topic file is !windows and is not built).
//
// Constraints: compiled locally (GOOS=windows go test -c) and run in the
//
//	windows/amd64 CI lane; sentinels by identity and message.
//
// SPORT: cmd/cascade chat reply windows (TEST) — P1-CPA-13e.
package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestChatReplyWindowsTierTwo(t *testing.T) {
	err := platformDaemonRun(context.Background(), daemonDeps{})
	want := resume.ErrWindowsUnsupported
	if err != want || err.Error() != want.Error() { //nolint:errorlint // identity, not errors.Is (Kind only)
		t.Fatalf("daemon run = %v, want the tier-2 sentinel %q", err, want)
	}
	_, err = platformDaemonStart(context.Background(), daemonDeps{})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindUnsupported ||
		!strings.HasPrefix(ce.Msg, "daemon start: cascade has no daemon on Windows (tier-2)") {
		t.Fatalf("daemon start = %v, want the typed tier-2 daemon refusal", err)
	}
}
