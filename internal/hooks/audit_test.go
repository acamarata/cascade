// Purpose: audit.go coverage — redactSecrets' unit behaviour (a
//
//	secret-shaped param value literally present in an error message is
//	stripped; a non-secret-shaped value is left alone) and an end-to-end
//	proof, through a real Dispatcher/Bus, that a HookFire's raw
//	ActionParams never reach the wire at all (only ParamsHash does) and
//	that a secret-shaped param value echoed back in a dispatch error is
//	redacted before publication.
//
// Constraints: white-box (package hooks) — redactSecrets is unexported.
//
//	Art.7.1: no filesystem use in this file (MemStore-backed bus, as in
//	dispatcher_test.go).
//
// SPORT: internal.hooks.HookFire/ADDED (audit path, tests)
//
//	(P1-E03-W1-S05-T1).
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRedactSecrets_StripsKnownSecretShapedValue(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	msg := fmt.Sprintf("plugin call failed: invalid key %s supplied", secret)
	params := map[string]string{"api_key": secret}

	got := redactSecrets(msg, params)
	if got == msg {
		t.Fatal("redactSecrets did not modify a message containing a secret-shaped param value")
	}
	if containsSubstring(got, secret) {
		t.Fatalf("redacted message still contains the secret: %q", got)
	}
	if !containsSubstring(got, "[REDACTED]") {
		t.Fatalf("redacted message missing [REDACTED] marker: %q", got)
	}
}

func TestRedactSecrets_LeavesNonSecretShapedValueAlone(t *testing.T) {
	msg := "plugin call failed: tool not found"
	params := map[string]string{"tool": "search"}

	got := redactSecrets(msg, params)
	if got != msg {
		t.Fatalf("redactSecrets modified a message with no secret-shaped params: got %q, want unchanged %q", got, msg)
	}
}

func TestRedactSecrets_EmptyParamValueSkipped(t *testing.T) {
	// An empty-string param must never be treated as a "secret" whose
	// (zero-length) occurrence gets replaced everywhere — that would
	// corrupt the message.
	msg := "plugin call failed: no reason given"
	params := map[string]string{"reason": ""}
	got := redactSecrets(msg, params)
	if got != msg {
		t.Fatalf("redactSecrets mishandled an empty param value: got %q, want unchanged %q", got, msg)
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return len(substr) == 0
}

// TestEmitAudit_NeverCarriesRawActionParams proves HookFire's wire payload
// carries ParamsHash only — the raw ActionParams map is never marshaled
// into the audit event at all, regardless of whether any value looks
// secret-shaped. This is audit.go's structural guarantee, distinct from
// (and stronger than) the ErrMsg-redaction heuristic.
func TestEmitAudit_NeverCarriesRawActionParams(t *testing.T) {
	secret := "AKIA1234567890ABCDEF"
	hook := HookConfig{
		ID:           "h1",
		Trigger:      "t",
		ActionType:   ActionTypePluginCall,
		ActionParams: map[string]string{"aws_key": secret},
	}
	bus, clock := testBus(t)

	d := &Dispatcher{bus: bus, clock: clock, auditNamespace: "audit"}

	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	sub, err := bus.Subscribe(ctx, "audit", "raw-params-check", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	d.emitAudit(hook, hookOutcome{result: ResultSuccess})

	select {
	case ev := <-sub.Events:
		if containsSubstring(string(ev.Payload), secret) {
			t.Fatalf("audit payload contains the raw secret value: %s", ev.Payload)
		}
		var fire HookFire
		if err := json.Unmarshal(ev.Payload, &fire); err != nil {
			t.Fatalf("decode HookFire: %v", err)
		}
		if fire.ParamsHash == "" {
			t.Fatal("ParamsHash is empty")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for audit event")
	}
}

// TestEmitAudit_RedactsSecretInErrMsg proves a dispatch error whose text
// echoes back a secret-shaped action_param value is redacted before the
// HookFire reaches the bus.
func TestEmitAudit_RedactsSecretInErrMsg(t *testing.T) {
	secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	hook := HookConfig{
		ID:           "h2",
		Trigger:      "t",
		ActionType:   ActionTypePluginCall,
		ActionParams: map[string]string{"token": secret},
	}
	bus, clock := testBus(t)

	d := &Dispatcher{bus: bus, clock: clock, auditNamespace: "audit"}

	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	sub, err := bus.Subscribe(ctx, "audit", "errmsg-redact-check", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	d.emitAudit(hook, hookOutcome{result: ResultError, err: errors.New("upstream rejected token " + secret)})

	select {
	case ev := <-sub.Events:
		if containsSubstring(string(ev.Payload), secret) {
			t.Fatalf("audit payload leaks the secret via ErrMsg: %s", ev.Payload)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for audit event")
	}
}
