//go:build integration

// Purpose: the Art.2 PRIMARY conformance evidence for cascade-pbd's SSE
//
//	bridge (pbd.go's bus/eventsHandler): starts a REAL TCP listener
//	(httptest.NewServer) hosting NewEventsHandler over a real
//	providers/sqlite-backed Projector, then drives it with `curl -N`, an
//	external process with its own HTTP/1.1 client and no awareness of
//	this package's streamEvents — parsing curl's captured stdout with a
//	second, independent line scanner proves the wire bytes are
//	conformant, not merely that this package's own writer and a
//	self-authored reader agree with each other. Mirrors
//	internal/fleet/sessions/sse_integration_test.go's proven shape.
//	Requires curl and network loopback; both are on every darwin/linux
//	CI runner this repo targets. Also proves the goroutine/subscription
//	cleanup this ticket's contract requires: after the client disconnects
//	the bus's subscriber map is empty, so nothing was leaked.
//
// SPORT: plugins.pbd.NewEventsHandler/ADD (P1-E14-W3-S29-T1).
package pbd

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
	"github.com/acamarata/cascade/providers/sqlite"
)

// testClock is a local, constant Clock double — never the wall clock.
type testClock struct{}

func (testClock) Now() time.Time { return time.Unix(1_700_000_000, 0) }

// writeIntegrationTicket writes one minimal, fully valid PEWS ticket file
// at its canonical tree position under root.
func writeIntegrationTicket(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "epics", "E-N", "waves", "W-3", "sprints", "S-29", "tickets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const doc = `id: P1-E14-W3-S29-T9
title: t
short_desc: d
full_desc: d
branch: b
weight: S
model_class: build
depends_on: []
tasks: []
checks: []
acceptance_criteria: []
files_scope:
  add: []
  change: []
  delete: []
spec_refs: []
cr_level: CR-B
qa_level: QA-A
sport_updates: []
docs_updates: []
`
	if err := os.WriteFile(filepath.Join(dir, "T-9.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write ticket: %v", err)
	}
}

// newIntegrationServer builds a real HTTP server hosting NewEventsHandler
// over a real providers/sqlite-backed Projector, plus a tree root with
// one ticket already on disk, ready for the caller to Project.
func newIntegrationServer(t *testing.T) (*httptest.Server, *eventsHandler, *pews.Projector, string) {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), t.TempDir()+"/pbd-events.db")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	handler, proj, err := NewEventsHandler(driver, testClock{})
	if err != nil {
		t.Fatalf("NewEventsHandler: %v", err)
	}
	root := t.TempDir()
	writeIntegrationTicket(t, root)
	return httptest.NewServer(handler), handler.(*eventsHandler), proj, root
}

// TestProjectionSSERealCounterpart exercises the wired GET /events
// endpoint with curl, an independent real client, never a self-authored
// SSE dialect.
func TestProjectionSSERealCounterpart(t *testing.T) {
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not available on this runner; PRIMARY SSE conformance evidence requires an independent client")
	}

	server, realHandler, proj, root := newIntegrationServer(t)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, curlPath, "-N", "-s", server.URL+eventsPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting curl: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	// Give curl time to complete the handshake before the projection
	// runs, so the event this test cares about is not published before
	// curl subscribes.
	time.Sleep(200 * time.Millisecond)

	if _, err := proj.Project(context.Background(), root, "P1"); err != nil {
		t.Fatalf("Project: %v", err)
	}

	got := readOneSSERecord(t, stdout)
	cancel()
	_ = cmd.Wait()

	if got["id"] != "P1" {
		t.Errorf("id field = %q, want the projected phase %q", got["id"], "P1")
	}
	if !strings.Contains(got["data"], `"P1-E14-W3-S29-T9"`) {
		t.Errorf("data field = %q, want it to contain the upserted ticket id", got["data"])
	}

	waitForNoSubscribers(t, realHandler.bus)
}

// waitForNoSubscribers polls until b's subscriber map is empty, proving
// ServeHTTP's deferred unsubscribe ran and no subscription (or the
// goroutine serving it) was leaked past client disconnect.
func waitForNoSubscribers(t *testing.T, b *bus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.subs)
		b.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bus still has subscribers after client disconnect; unsubscribe leaked")
}

// readOneSSERecord scans stdout for the first complete "id/data" record,
// terminated by a blank line — an independent parser, deliberately not
// sharing any code with streamEvents.
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
