//go:build integration

// Purpose: the Art.2 PRIMARY conformance evidence for the SSE surface
//
//	(per this ticket's contract: "a REAL, independent SSE client... that
//	does NOT share the server's parser"). Starts a REAL TCP listener
//	(httptest.NewServer) hosting sessions.SSEHandler over a REAL
//	internal/events.Bus, then drives it with `curl -N`, an external
//	process with its own HTTP/1.1 client and no awareness of this
//	package's writeSSEEvent — parsing curl's captured stdout with a
//	second, independent line scanner proves the wire bytes are
//	conformant, not merely that this package's own writer and reader
//	agree with each other (the self-authored-dialect failure Art.2
//	exists to prevent). Requires curl and network loopback; both are
//	available on every darwin/linux CI runner this repo targets.
//
// SPORT: internal.fleet.sessions.SSEHandler/ADDED (P1-E12-W3-S24-T3).
package sessions_test

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestSessionsSSEIntegration exercises the full path S-24.T1's census
// Upsert would drive in production: a real Store.Upsert publishes to a
// real Bus, and a real independent SSE client (curl, an external process)
// observes the resulting fleet.sessions.changed event over a real
// socket.
func TestSessionsSSEIntegration(t *testing.T) {
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not available on this runner; PRIMARY SSE conformance evidence requires an independent client")
	}

	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	handler := sessions.NewSSEHandler(bus, clock)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	store := sessions.New(storetest.NewMemStore(), clock, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, curlPath, "-N", "-s", server.URL+sessions.EventsPath+"?topic=fleet.sessions")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting curl: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	// Give curl time to complete the handshake before publishing, so the
	// event this test cares about is not published before curl subscribes.
	time.Sleep(200 * time.Millisecond)

	if err := store.Upsert(context.Background(), sessions.SessionRecord{
		SessionID: "integration-s1", Harness: "claude", Account: "a1", PID: 4242, State: "running",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got := readOneSSERecord(t, stdout)
	cancel()

	assertField(t, got, "event", "fleet.sessions.changed")
	if !strings.Contains(got["data"], `"session_id":"integration-s1"`) {
		t.Errorf("data field = %q, want it to contain the upserted session_id", got["data"])
	}
	if got["id"] == "" {
		t.Errorf("id field missing; got record: %+v", got)
	}
	assertField(t, got, "retry", "3000")
}

// readOneSSERecord scans stdout for the first complete "event/id/data/
// retry" record, terminated by a blank line — an independent parser from
// this package's own writeSSEEvent, deliberately not sharing any code
// with it.
func readOneSSERecord(t *testing.T, stdout io.Reader) map[string]string {
	t.Helper()
	scanner := bufio.NewScanner(stdout)
	fields := make(map[string]string)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(fields) > 0 {
				return fields
			}
			continue
		}
		name, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		fields[name] = value
	}
	t.Fatalf("SSE stream ended before a complete record was received; fields so far: %+v", fields)
	return nil
}

func assertField(t *testing.T, got map[string]string, name, want string) {
	t.Helper()
	if got[name] != want {
		t.Errorf("%s field = %q, want %q", name, got[name], want)
	}
}
