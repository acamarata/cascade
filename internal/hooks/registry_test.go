// Purpose: Registry coverage — registration happy path for both permitted
//
//	action types, shell/unknown registration refusal, duplicate/empty-
//	trigger/reserved-trigger refusal, Deregister, List/MatchTriggers
//	ordering and miss behaviour, and concurrent Register/Deregister
//	(-race).
//
// Constraints: Art.7.1 — no test in this package writes to disk; Registry
//
//	is pure in-memory state, so none of these tests need t.TempDir().
//
// SPORT: internal.hooks.Registry/ADDED (tests) (P1-E03-W1-S05-T1).
package hooks_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRegistry_Register_PluginCall_HappyPath(t *testing.T) {
	r := hooks.NewRegistry()
	got, err := r.Register(hooks.HookConfig{
		Trigger:      "plugin.registered",
		ActionType:   hooks.ActionTypePluginCall,
		ActionParams: map[string]string{"plugin": "p1"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.ID == "" {
		t.Fatal("Register: derived ID is empty")
	}
	if len(r.List()) != 1 {
		t.Fatalf("List: got %d hooks, want 1", len(r.List()))
	}
}

func TestRegistry_Register_AgentNote_HappyPath(t *testing.T) {
	r := hooks.NewRegistry()
	got, err := r.Register(hooks.HookConfig{
		ID:           "my-note-hook",
		Trigger:      "scheduler.tick",
		ActionType:   hooks.ActionTypeAgentNote,
		ActionParams: map[string]string{"note": "tick"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.ID != "my-note-hook" {
		t.Fatalf("Register: ID = %q, want explicit ID preserved", got.ID)
	}
}

func TestHooksShellActionRegistrationRefused(t *testing.T) {
	r := hooks.NewRegistry()
	_, err := r.Register(hooks.HookConfig{
		Trigger:    "plugin.registered",
		ActionType: hooks.ActionTypeShell,
	})
	assertActionNotPermitted(t, err)
	if len(r.List()) != 0 {
		t.Fatalf("List: got %d hooks after refused registration, want 0", len(r.List()))
	}
}

func TestRegistry_Register_UnknownActionType_Refused(t *testing.T) {
	r := hooks.NewRegistry()
	_, err := r.Register(hooks.HookConfig{
		Trigger:    "plugin.registered",
		ActionType: hooks.ActionType("carrier-pigeon"),
	})
	assertActionNotPermitted(t, err)
	if len(r.List()) != 0 {
		t.Fatalf("List: got %d hooks after refused registration, want 0", len(r.List()))
	}
}

func TestRegistry_Register_EmptyTrigger_Refused(t *testing.T) {
	r := hooks.NewRegistry()
	_, err := r.Register(hooks.HookConfig{ActionType: hooks.ActionTypePluginCall})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Register(empty trigger) error = %v, want KindInvalidInput", err)
	}
}

func TestRegistry_Register_ReservedAuditTrigger_Refused(t *testing.T) {
	r := hooks.NewRegistry()
	_, err := r.Register(hooks.HookConfig{
		Trigger:    string(hooks.EventKindHookFire),
		ActionType: hooks.ActionTypePluginCall,
	})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Register(reserved trigger) error = %v, want KindInvalidInput", err)
	}
}

func TestRegistry_Register_DuplicateID_Conflict(t *testing.T) {
	r := hooks.NewRegistry()
	cfg := hooks.HookConfig{ID: "dup", Trigger: "t1", ActionType: hooks.ActionTypePluginCall}
	if _, err := r.Register(cfg); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, err := r.Register(cfg)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("second Register error = %v, want KindConflict", err)
	}
}

func TestRegistry_Deregister(t *testing.T) {
	r := hooks.NewRegistry()
	cfg, err := r.Register(hooks.HookConfig{Trigger: "t1", ActionType: hooks.ActionTypePluginCall})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Deregister(cfg.ID); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if len(r.List()) != 0 {
		t.Fatalf("List after Deregister: got %d, want 0", len(r.List()))
	}
}

func TestRegistry_Deregister_NotFound(t *testing.T) {
	r := hooks.NewRegistry()
	err := r.Deregister("nope")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("Deregister(missing) error = %v, want KindNotFound", err)
	}
}

func TestRegistry_MatchTriggers_HitAndMiss(t *testing.T) {
	r := hooks.NewRegistry()
	if _, err := r.Register(hooks.HookConfig{ID: "a", Trigger: "hit", ActionType: hooks.ActionTypePluginCall}); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if _, err := r.Register(hooks.HookConfig{ID: "b", Trigger: "hit", ActionType: hooks.ActionTypeAgentNote}); err != nil {
		t.Fatalf("Register b: %v", err)
	}
	if _, err := r.Register(hooks.HookConfig{ID: "c", Trigger: "miss-me", ActionType: hooks.ActionTypePluginCall}); err != nil {
		t.Fatalf("Register c: %v", err)
	}

	hits := r.MatchTriggers("hit")
	if len(hits) != 2 || hits[0].ID != "a" || hits[1].ID != "b" {
		t.Fatalf("MatchTriggers(hit) = %+v, want [a b] sorted", hits)
	}

	misses := r.MatchTriggers("no-such-trigger")
	if len(misses) != 0 {
		t.Fatalf("MatchTriggers(no-such-trigger) = %+v, want empty", misses)
	}
}

func TestRegistry_ConcurrentRegisterDeregister(t *testing.T) {
	r := hooks.NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine's params differ (distinct "n" per i<20), so
			// DeriveHookID gives each a distinct ID — Register must never
			// fail here with -race clean concurrent access.
			cfg, err := r.Register(hooks.HookConfig{
				Trigger:    "concurrent",
				ActionType: hooks.ActionTypePluginCall,
				ActionParams: map[string]string{
					"n": string(rune('a' + i%26)),
				},
			})
			if err != nil {
				t.Errorf("concurrent Register(%d): %v", i, err)
				return
			}
			if err := r.Deregister(cfg.ID); err != nil {
				t.Errorf("concurrent Deregister(%d): %v", i, err)
			}
		}(i)
	}
	_ = r.List()
	_ = r.MatchTriggers("concurrent")
	wg.Wait()
}

// assertActionNotPermitted asserts err is the package's action-not-
// permitted refusal: KindPolicyDenied, carrying HookActionNotPermittedCode.
func assertActionNotPermitted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want action-not-permitted refusal")
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		t.Fatalf("error = %v (%T), want a *cascade.Error", err, err)
	}
	if kind != cascade.KindPolicyDenied {
		t.Fatalf("error Kind = %v, want KindPolicyDenied", kind)
	}
	if got := err.Error(); !strings.Contains(got, hooks.HookActionNotPermittedCode) {
		t.Fatalf("error message %q does not contain %q", got, hooks.HookActionNotPermittedCode)
	}
}
