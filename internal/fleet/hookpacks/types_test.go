// Purpose: the HookEventType closed-set fail-closed lookup, SessionsPack's
//
//	five-event descriptor set with its captured fixtures, the rendered
//	plugin file, and HookPayload's wire allowlist shape (exactly six
//	fields, nothing else survives a marshal round trip).
//
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// sessionsFixtureDir holds the captured harness payloads; the provenance
// table lives in testdata/README.md.
const sessionsFixtureDir = "testdata/cc-hook-fixtures"

// sessionsPackFile is the rendered pack the claude plugin ships, relative to
// this package.
const sessionsPackFile = "../../../plugins/claude/hookpacks/sessions.json"

// socketToken is the placeholder the registry renders against.
const socketToken = "{{CASCADE_SOCKET_PATH}}"

// provenanceRow finds fixture's row in README.md's provenance table and
// returns its cells (fixture, client version, capture date, event).
func provenanceRow(t *testing.T, readme, fixture string) []string {
	t.Helper()
	for _, line := range strings.Split(readme, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) >= 4 && strings.TrimSpace(cells[0]) == fixture {
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			return cells
		}
	}
	return nil
}

// TestSessionsPackInstallsFiveCapturedEvents proves the pack installs exactly
// the five events a captured fixture backs, each descriptor has a fixture file
// with a provenance row, and the pre-fix curl template is gone.
func TestSessionsPackInstallsFiveCapturedEvents(t *testing.T) {
	pack := hookpacks.SessionsPack()
	if pack.Name != "sessions" {
		t.Fatalf("Name = %q, want %q", pack.Name, "sessions")
	}
	want := []hookpacks.HookEventType{
		hookpacks.EventSessionStart, hookpacks.EventPreToolUse, hookpacks.EventPostToolUse,
		hookpacks.EventStop, hookpacks.EventSessionEnd,
	}
	if len(pack.Descriptors) != len(want) {
		t.Fatalf("Descriptors = %+v, want exactly %v", pack.Descriptors, want)
	}
	readmeRaw, err := os.ReadFile(filepath.Join("testdata", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	sessionIDs := map[string]bool{}
	for i, d := range pack.Descriptors {
		if d.EventType != want[i] {
			t.Fatalf("descriptor %d event = %q, want %q", i, d.EventType, want[i])
		}
		if cmd := "cascade fleet sessions hook-event " + string(d.EventType); d.CommandTemplate != cmd {
			t.Fatalf("%s command = %q, want %q", d.EventType, d.CommandTemplate, cmd)
		}
		if d.TimeoutSeconds != 5 || hookpacks.SessionsHookTimeoutSeconds != 5 {
			t.Fatalf("%s timeout = %d, want 5", d.EventType, d.TimeoutSeconds)
		}
		if strings.Contains(d.CommandTemplate, "curl") {
			t.Fatalf("%s still installs the curl template: %q", d.EventType, d.CommandTemplate)
		}
		fixture := strings.ToLower(string(d.EventType)) + ".json"
		raw, err := os.ReadFile(filepath.Join(sessionsFixtureDir, fixture))
		if err != nil {
			t.Fatalf("%s has no captured fixture: %v", d.EventType, err)
		}
		var native struct {
			SessionID string `json:"session_id"`
			EventName string `json:"hook_event_name"`
		}
		if err := json.Unmarshal(raw, &native); err != nil || native.SessionID == "" || native.EventName != string(d.EventType) {
			t.Fatalf("%s fixture decodes to %+v (err %v), want its own event name and a session_id", fixture, native, err)
		}
		sessionIDs[native.SessionID] = true
		row := provenanceRow(t, string(readmeRaw), fixture)
		if row == nil || row[1] == "" || row[2] == "" || row[3] != string(d.EventType) {
			t.Fatalf("%s has no complete provenance row in testdata/README.md: %v", fixture, row)
		}
	}
	if len(sessionIDs) != 1 {
		t.Fatalf("the five fixtures carry %d session ids, want one captured session: %v", len(sessionIDs), sessionIDs)
	}
}

// TestSessionsPackFileMatchesRender pins plugins/claude/hookpacks/sessions.json
// to exactly what a registry holding only SessionsPack renders.
func TestSessionsPackFileMatchesRender(t *testing.T) {
	registry := hookpacks.NewHookRegistry()
	registry.RegisterPack("sessions", hookpacks.SessionsPack())
	rendered := registry.Render(socketToken)
	if len(rendered) == 0 {
		t.Fatal("registry rendered nothing for the placeholder socket")
	}
	onDisk, err := os.ReadFile(sessionsPackFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, rendered) {
		t.Fatalf("%s differs from the registry's render:\n--- file\n%s\n--- render\n%s", sessionsPackFile, onDisk, rendered)
	}
}

// TestSessionsHookEventsMatchesPackAndIsACopy pins SessionsHookEvents to the
// pack's install order and proves a caller cannot mutate the closed set.
func TestSessionsHookEventsMatchesPackAndIsACopy(t *testing.T) {
	events := hookpacks.SessionsHookEvents()
	descriptors := hookpacks.SessionsPack().Descriptors
	if len(events) != len(descriptors) {
		t.Fatalf("SessionsHookEvents has %d events, SessionsPack installs %d", len(events), len(descriptors))
	}
	for i, d := range descriptors {
		if events[i] != d.EventType {
			t.Fatalf("event %d = %q, pack installs %q", i, events[i], d.EventType)
		}
	}
	events[0] = "Mutated"
	if got := hookpacks.SessionsHookEvents()[0]; got != descriptors[0].EventType {
		t.Fatalf("mutating the returned slice changed the closed set: %q", got)
	}
}

func TestHookPack_ParseHookEventTypeFailsClosedOnUnknown(t *testing.T) {
	if !hookpacks.ParseHookEventType(hookpacks.EventStop) {
		t.Fatal("ParseHookEventType(EventStop) = false, want true")
	}
	if hookpacks.ParseHookEventType(hookpacks.HookEventType("TotallyMadeUp")) {
		t.Fatal("ParseHookEventType(\"TotallyMadeUp\") = true, want false (fail closed)")
	}
	if hookpacks.ParseHookEventType("") {
		t.Fatal("ParseHookEventType(\"\") = true, want false")
	}
}

// TestHookPayload_WireShapeIsExactlySixFields proves the allowlist is
// structural: encoding a fully-populated HookPayload and decoding it
// back into a bare map yields exactly these six keys, never more —
// there is no field on the Go struct through which any other value
// could ever be marshaled out.
func TestHookPayload_WireShapeIsExactlySixFields(t *testing.T) {
	p := hookpacks.HookPayload{
		Harness: "test-harness", EventType: hookpacks.EventPreToolUse,
		SessionID: "s1", PID: 7, Account: "acct", TimestampMs: 1_700_000_000_000,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	wantKeys := []string{"harness", "event_type", "session_id", "pid", "account", "timestamp_ms"}
	if len(asMap) != len(wantKeys) {
		t.Fatalf("marshaled keys = %v, want exactly %v", keysOf(asMap), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := asMap[k]; !ok {
			t.Fatalf("missing expected key %q in %v", k, keysOf(asMap))
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
