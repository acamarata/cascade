// Purpose: ParseHooksSection strictness. Every bad shape refuses the whole
//
//	section, naming the entry index and key and never the value; the
//	runnable predicate is the load gate for action types.
package hooks

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// goodEntry is a valid [[hooks]] table.
func goodEntry() map[string]any {
	return map[string]any{"namespace": "jobs.gate", "trigger": "job.done", "action_type": "plugin-call",
		"action_params": map[string]any{"plugin": "p", "tool": "t"}}
}

func allRunnable(t ActionType) bool { return t == ActionTypePluginCall || t == ActionTypeAgentNote }

// TestParseHooksRefusesInvalidEntry puts a valid entry at index 0 and one
// bad shape at index 1: the whole section is refused with KindInvalidInput
// naming entry 1 and the key, and nothing is returned.
func TestParseHooksRefusesInvalidEntry(t *testing.T) {
	const leak = "SECRETVALUE"
	with := func(k string, v any) any {
		e := goodEntry()
		e[k] = v
		return e
	}
	without := func(k string) any {
		e := goodEntry()
		delete(e, k)
		return e
	}
	cases := []struct {
		name, key string
		entry     any
	}{
		{"unknown key", "command", with("command", leak)},
		{"not a table", "", leak},
		{"namespace wrong type", "namespace", with("namespace", 7)},
		{"namespace missing", "namespace", without("namespace")},
		{"namespace empty", "namespace", with("namespace", "")},
		{"namespace upper", "namespace", with("namespace", leak)},
		{"namespace leading digit", "namespace", with("namespace", "9jobs")},
		{"namespace too long", "namespace", with("namespace", "a"+strings.Repeat("b", 64))},
		{"namespace is audit", "namespace", with("namespace", AuditNamespace)},
		{"trigger wrong type", "trigger", with("trigger", true)},
		{"trigger empty", "trigger", with("trigger", "")},
		{"trigger is audit kind", "trigger", with("trigger", string(EventKindHookFire))},
		{"action_type missing", "action_type", without("action_type")},
		{"action_type wrong type", "action_type", with("action_type", 3)},
		{"action_type unrunnable", "action_type", with("action_type", "carrier-pigeon")},
		{"action_params not table", "action_params", with("action_params", leak)},
		{"action_params non-string", "action_params", with("action_params", map[string]any{"k": 1})},
		{"id wrong type", "id", with("id", 5)},
		{"id empty", "id", with("id", "")},
		{"duplicate derived id", "id", goodEntry()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHooksSection([]any{goodEntry(), tc.entry}, allRunnable)
			if got != nil {
				t.Fatalf("a refused section returned %d entries", len(got))
			}
			assertSectionError(t, err, tc.key)
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("the error echoes a configured value: %v", err)
			}
		})
	}
}

// assertSectionError checks identity (*cascade.Error, KindInvalidInput)
// and the message naming entry 1 and key.
func assertSectionError(t *testing.T, err error, key string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
		t.Fatalf("error = %v, want a KindInvalidInput *cascade.Error", err)
	}
	want := "hooks: [[hooks]] entry 1"
	if key != "" {
		want += " key \"" + key + "\""
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name %q", err.Error(), want)
	}
}

// TestParseHooksRefusesNonRunnableType uses a real dispatcher's Runnable:
// without a ShellRunner a shell entry refuses the section, with one it
// loads.
func TestParseHooksRefusesNonRunnableType(t *testing.T) {
	shellEntry := map[string]any{"namespace": "jobs", "trigger": "t", "action_type": "shell",
		"action_params": map[string]any{ShellCommandParam: "ls"}}
	without := newRig(t).build(nil)
	got, err := ParseHooksSection([]any{goodEntry(), shellEntry}, without.Runnable)
	if got != nil {
		t.Fatal("a section with an unrunnable shell entry returned entries")
	}
	assertSectionError(t, err, "action_type")

	with := newRig(t).build(func(c *DispatcherConfig) { c.ShellRunner, c.ShellCapability = &fakeShellRunner{}, "hooks.shell" })
	got, err = ParseHooksSection([]any{goodEntry(), shellEntry}, with.Runnable)
	if err != nil || len(got) != 2 || got[1].ActionType != ActionTypeShell {
		t.Fatalf("ParseHooksSection(shell runnable) = %+v, %v", got, err)
	}
	if _, err := ParseHooksSection([]any{goodEntry()}, nil); err == nil {
		t.Fatal("a nil runnable predicate was accepted")
	}
}

// TestParseHooksAbsentIsEmpty covers the absent section, an empty array,
// both array shapes, derived and explicit ids, and a non-array section.
func TestParseHooksAbsentIsEmpty(t *testing.T) {
	if got, err := ParseHooksSection(nil, allRunnable); got != nil || err != nil {
		t.Fatalf("ParseHooksSection(nil) = %v, %v; want nil, nil", got, err)
	}
	if got, err := ParseHooksSection([]any{}, allRunnable); err != nil || len(got) != 0 {
		t.Fatalf("ParseHooksSection(empty) = %v, %v", got, err)
	}
	explicit := goodEntry()
	explicit["id"] = "mine"
	got, err := ParseHooksSection([]map[string]any{goodEntry(), explicit}, allRunnable)
	if err != nil || len(got) != 2 {
		t.Fatalf("ParseHooksSection(tables) = %v, %v", got, err)
	}
	wantID := DeriveHookID("jobs.gate", "job.done", ActionTypePluginCall, map[string]string{"plugin": "p", "tool": "t"})
	if got[0].ID != wantID || got[1].ID != "mine" || got[0].Namespace != "jobs.gate" || got[0].ActionParams["tool"] != "t" {
		t.Fatalf("parsed = %+v", got)
	}
	strMap := goodEntry()
	strMap["action_params"] = map[string]string{"plugin": "q"}
	if got, err := ParseHooksSection([]any{strMap}, allRunnable); err != nil || got[0].ActionParams["plugin"] != "q" {
		t.Fatalf("string-map params = %+v, %v", got, err)
	}
	if _, err := ParseHooksSection(map[string]any{"namespace": "jobs"}, allRunnable); err == nil {
		t.Fatal("a single table (not an array) was accepted")
	}
}
