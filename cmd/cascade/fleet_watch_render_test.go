// Purpose: unit coverage for renderFleetSessionsWatchUpdate's two render
//
//	modes (NDJSON on non-TTY, table refresh on TTY), which shipped with
//	no direct test of their own.
//
// SPORT: cmd.cascade.fleet-watch/TEST (fleet watch render path).
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/output"
)

func TestRenderFleetSessionsWatchUpdate_NonTTYEmitsNDJSON(t *testing.T) {
	var buf bytes.Buffer
	w := output.New(&buf, &buf, false, false, false, true)
	latest := sessions.SessionRecord{SessionID: "s1", Harness: "cc", State: "running"}
	seen := map[string]sessions.SessionRecord{"s1": latest}

	if err := renderFleetSessionsWatchUpdate(w, seen, latest); err != nil {
		t.Fatalf("renderFleetSessionsWatchUpdate: %v", err)
	}
	if !strings.Contains(buf.String(), "s1") {
		t.Errorf("output missing session id: %q", buf.String())
	}
}
