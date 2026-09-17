package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/pkg/provider"
)

// realCCPayload reads the live capture. Its provenance is in
// testdata/cc-hook-fixtures/README.md.
func realCCPayload(t testing.TB) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cc-hook-fixtures", "userpromptsubmit.json"))
	if err != nil {
		t.Fatalf("read the captured UserPromptSubmit payload: %v", err)
	}
	return raw
}

// TestPromptHydrationRealCCFixture is the Art.2 assertion: the decoder
// reads the payload a REAL client really sent, field for field.
//
// It asserts the fixture is a live capture and not a reconstruction, by
// requiring the two fields that gave the capture away — prompt_id and
// permission_mode, neither of which appears in this repository's earlier
// hand-authored hook fixtures. A future edit that "tidied" the fixture
// into the shape someone remembered would fail here.
func TestPromptHydrationRealCCFixture(t *testing.T) {
	raw := realCCPayload(t)

	payload, ok := decodeHookPayload(bytes.NewReader(raw))
	if !ok {
		t.Fatal("the decoder refused a payload the real client sent")
	}
	if payload.HookEventName != userPromptSubmitEvent {
		t.Errorf("hook_event_name = %q, want %q", payload.HookEventName, userPromptSubmitEvent)
	}
	if payload.SessionID == "" || payload.Cwd == "" || payload.Prompt == "" {
		t.Fatalf("decoded payload is missing a field the capture carries: %+v", payload)
	}

	var everyField map[string]any
	if err := json.Unmarshal(raw, &everyField); err != nil {
		t.Fatalf("the fixture is not valid JSON: %v", err)
	}
	for _, name := range []string{"prompt_id", "permission_mode", "transcript_path"} {
		if _, present := everyField[name]; !present {
			t.Errorf("the fixture has no %q; this is the shape of a reconstruction, not a capture "+
				"(see testdata/cc-hook-fixtures/README.md)", name)
		}
	}
}

// TestHookPayloadIgnoresFieldsItDoesNotRead proves the decoder tolerates a
// newer client. A hook that refused an unknown field would take a user's
// context away at exactly the moment they upgraded.
func TestHookPayloadIgnoresFieldsItDoesNotRead(t *testing.T) {
	raw := `{"cwd":"/tmp/x","hook_event_name":"UserPromptSubmit","prompt":"hi","a_field_from_next_year":42}`
	if _, ok := decodeHookPayload(strings.NewReader(raw)); !ok {
		t.Fatal("the decoder refused a payload carrying one unknown field")
	}
}

// TestHookPayloadRefusesWhatItCannotActOn walks the three payloads that
// leave the hook with nothing to do.
func TestHookPayloadRefusesWhatItCannotActOn(t *testing.T) {
	for name, raw := range map[string]string{
		"empty stdin":  "",
		"not json":     "this is not json",
		"no cwd":       `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`,
		"empty object": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeHookPayload(strings.NewReader(raw)); ok {
				t.Fatal("the decoder accepted a payload it cannot act on")
			}
		})
	}
}

// TestContextSliceHookMinScoreFilter pins the R-16.6a floor, in both
// directions. Asserting only that low scores are dropped would pass for a
// filter that dropped everything.
func TestContextSliceHookMinScoreFilter(t *testing.T) {
	chunks := []provider.RetrievedChunk{
		{Path: "a.md", Score: 0.9},
		{Path: "b.md", Score: hydration.DefaultMinScore}, // exactly at the floor
		{Path: "c.md", Score: 0.34},
		{Path: "d.md", Score: 0},
	}
	kept := filterByScore(chunks, hydration.DefaultMinScore)
	if len(kept) != 2 {
		t.Fatalf("kept %d chunks, want the two at or above %v: %+v", len(kept), hydration.DefaultMinScore, kept)
	}
	if kept[0].Path != "a.md" || kept[1].Path != "b.md" {
		t.Fatalf("kept %+v, want a.md and b.md in rank order", kept)
	}
	if all := filterByScore(chunks, 0); len(all) != len(chunks) {
		t.Fatalf("a zero floor dropped %d of %d chunks", len(chunks)-len(all), len(chunks))
	}
}

// TestContextSliceHookInjection asserts the emitted object is exactly the
// shape the real client accepted, and that the capsule's first line is
// R-16.6a's fixed header.
func TestContextSliceHookInjection(t *testing.T) {
	capsule := renderCapsule("repository/cascade", []provider.RetrievedChunk{
		{Path: "docs/a.md", Score: 0.9}, {Path: "docs/b.md", Score: 0.5},
	})
	if first, _, _ := strings.Cut(capsule, "\n"); first != "Cascade context (scope: repository/cascade, 2 items)" {
		t.Fatalf("capsule first line = %q", first)
	}

	var out bytes.Buffer
	if err := emitHookInjection(&out, capsule); err != nil {
		t.Fatalf("emitHookInjection: %v", err)
	}
	var decoded hookInjection
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("the emitted object is not valid JSON: %v\n%s", err, out.String())
	}
	if decoded.HookSpecificOutput.HookEventName != userPromptSubmitEvent {
		t.Errorf("hookEventName = %q", decoded.HookSpecificOutput.HookEventName)
	}
	if decoded.HookSpecificOutput.AdditionalContext != capsule {
		t.Errorf("additionalContext was altered in transit")
	}
	// The wire key names are the client's, not Go's. Asserting the
	// decoded struct alone would pass for a struct whose tags were wrong.
	for _, key := range []string{`"hookSpecificOutput"`, `"hookEventName"`, `"additionalContext"`} {
		if !strings.Contains(out.String(), key) {
			t.Errorf("the emitted object has no %s key: %s", key, out.String())
		}
	}
}

// TestCapsuleNamesEveryKeptChunk proves the capsule reports what it
// filtered to, not a count over one thing and a body over another.
func TestCapsuleNamesEveryKeptChunk(t *testing.T) {
	chunks := []provider.RetrievedChunk{{Path: "one.md"}, {Path: "two.md"}, {Path: "three.md"}}
	capsule := renderCapsule("project/demo", chunks)
	if !strings.Contains(capsule, ", 3 items)") {
		t.Errorf("header does not count three items: %q", capsule)
	}
	for _, c := range chunks {
		if !strings.Contains(capsule, c.Path) {
			t.Errorf("capsule omits %q:\n%s", c.Path, capsule)
		}
	}
}

// TestBaseNameHandlesTheShapesAScopeProduces covers the repository-root
// spellings the capsule header renders.
func TestBaseNameHandlesTheShapesAScopeProduces(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b/cascade":  "cascade",
		"/a/b/cascade/": "cascade",
		`C:\src\demo`:   "demo",
		"cascade":       "cascade",
		"/":             "general",
		"":              "general",
	} {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzUserPromptSubmitPayload proves the decoder never panics on untrusted
// stdin. The payload comes from another program over a pipe, and a hook
// that crashed on a malformed one would surface to the user as a broken
// prompt.
func FuzzUserPromptSubmitPayload(f *testing.F) {
	f.Add(string(realCCPayload(f)))
	f.Add(`{}`)
	f.Add(`{"cwd":123}`)
	f.Add(``)
	f.Add(`{"cwd":"/tmp","prompt":"\u0000"}`)
	f.Fuzz(func(t *testing.T, raw string) {
		payload, ok := decodeHookPayload(strings.NewReader(raw))
		if ok && payload.Cwd == "" {
			t.Fatalf("accepted a payload with no cwd: %q", raw)
		}
	})
}
