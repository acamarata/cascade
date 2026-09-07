// Purpose: HookRegistry's RegisterPack/Packs/Render union behavior
//
//	(R-16.48), plus proof the package-level DefaultRegistry already
//	carries this ticket's own "sessions" pack from init().
//
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks_test

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func packWithOneDescriptor(name string, evt hookpacks.HookEventType) hookpacks.HookPack {
	return hookpacks.HookPack{
		Name: name,
		Descriptors: []hookpacks.HookDescriptor{
			{EventType: evt, Matcher: "", CommandTemplate: "echo {{CASCADE_SOCKET_PATH}} || true"},
		},
	}
}

func TestHookPackRegistry_RegisterAndPacksPreserveOrder(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	reg.RegisterPack("first", packWithOneDescriptor("first", hookpacks.EventStop))
	reg.RegisterPack("second", packWithOneDescriptor("second", hookpacks.EventPreToolUse))

	packs := reg.Packs()
	if len(packs) != 2 || packs[0].Name != "first" || packs[1].Name != "second" {
		t.Fatalf("Packs() = %+v, want [first, second] in registration order", packs)
	}
}

func TestHookPackRegistry_ReRegisterKeepsPositionOverwritesContent(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	reg.RegisterPack("a", packWithOneDescriptor("a", hookpacks.EventStop))
	reg.RegisterPack("b", packWithOneDescriptor("b", hookpacks.EventPreToolUse))
	reg.RegisterPack("a", packWithOneDescriptor("a", hookpacks.EventPostToolUse))

	packs := reg.Packs()
	if len(packs) != 2 || packs[0].Name != "a" || packs[1].Name != "b" {
		t.Fatalf("Packs() = %+v, want [a, b] with a's original position kept", packs)
	}
	if packs[0].Descriptors[0].EventType != hookpacks.EventPostToolUse {
		t.Fatalf("re-registered pack a's descriptor = %+v, want the overwritten PostToolUse content", packs[0].Descriptors[0])
	}
}

func TestHookPackRegistry_RenderUnionsMultiplePacks(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	reg.RegisterPack("a", packWithOneDescriptor("a", hookpacks.EventStop))
	reg.RegisterPack("b", packWithOneDescriptor("b", hookpacks.EventPreToolUse))

	out := reg.Render("/tmp/example.sock")
	if out == nil {
		t.Fatal("Render returned nil for a valid socket path")
	}
	var decoded struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("Render output does not parse: %v", err)
	}
	if len(decoded.Hooks["Stop"]) != 1 || len(decoded.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("Hooks = %+v, want exactly one Stop entry and one PreToolUse entry", decoded.Hooks)
	}
}

func TestHookPackRegistry_RenderEmptySocketNeverPanics(t *testing.T) {
	reg := hookpacks.NewHookRegistry()
	reg.RegisterPack("a", packWithOneDescriptor("a", hookpacks.EventStop))
	if out := reg.Render(""); out != nil {
		t.Fatalf("Render(\"\") = %s, want nil (fail-closed, never a partial config)", out)
	}
}

func TestHookPackRegistry_DefaultRegistryHasSessionsPack(t *testing.T) {
	found := false
	for _, p := range hookpacks.DefaultRegistry.Packs() {
		if p.Name == "sessions" {
			found = true
			if len(p.Descriptors) != 3 {
				t.Fatalf("sessions pack has %d descriptors, want 3", len(p.Descriptors))
			}
		}
	}
	if !found {
		t.Fatal("DefaultRegistry.Packs() does not contain the \"sessions\" pack registered by init()")
	}
}
