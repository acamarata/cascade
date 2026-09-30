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
	"errors"
	"fmt"
	"testing"
)

func TestRedactSecrets_StripsKnownSecretShapedValue(t *testing.T) {
	secret := "sk-" + "abcdefghijklmnopqrstuvwxyz0123456789"
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

// TestEmitAudit_NeverCarriesRawActionParams proves the recorded HookFire
// carries the tagged params hash only: the raw configured value never
// reaches the payload, and the hash is not the raw params' hash.
func TestEmitAudit_NeverCarriesRawActionParams(t *testing.T) {
	secret := "AKIA" + "1234567890ABCDEF"
	r := newRig(t)
	r.build(nil)
	sub := r.audit()
	hook := HookConfig{ID: "h1", Namespace: "jobs", Trigger: "t", ActionType: ActionTypePluginCall,
		ActionParams: map[string]string{"aws_key": secret}}
	st := &seamState{}
	st.setTagged(map[string]string{"aws_key": "<tag>"})
	st.finish()
	_, _ = r.d.record(hook, Fire{Hook: hook, Namespace: "jobs"}, hookOutcome{result: ResultSuccess}, st)
	fire, ev := nextFire(t, sub)
	if containsSubstring(string(ev.Payload), secret) {
		t.Fatalf("audit payload contains the raw secret value: %s", ev.Payload)
	}
	if fire.ParamsHash != paramsHash(map[string]string{"aws_key": "<tag>"}) || fire.ParamsHash == paramsHash(hook.ActionParams) {
		t.Fatalf("ParamsHash = %q, want the tagged params hash", fire.ParamsHash)
	}
}

// TestEmitAudit_RedactsSecretInErrMsg proves a runner error echoing a
// secret-shaped configured value is redacted before the HookFire exists.
func TestEmitAudit_RedactsSecretInErrMsg(t *testing.T) {
	secret := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	r := newRig(t)
	r.build(nil)
	sub := r.audit()
	hook := HookConfig{ID: "h2", Namespace: "jobs", Trigger: "t", ActionType: ActionTypePluginCall,
		ActionParams: map[string]string{"token": secret}}
	_, err := r.d.record(hook, Fire{Hook: hook}, hookOutcome{result: ResultError,
		err: errors.New("upstream rejected token " + secret)}, nil)
	_, ev := nextFire(t, sub)
	if containsSubstring(string(ev.Payload), secret) || containsSubstring(err.Error(), secret) {
		t.Fatalf("the secret leaked: payload %s err %v", ev.Payload, err)
	}
	if fires := r.d.Fires(); len(fires) != 1 || containsSubstring(fires[0].ErrMsg, secret) {
		t.Fatalf("fires ring = %+v", fires)
	}
}

// TestScrubCatchesAFragmentOfARehydratedValue proves a plaintext quoted
// without the text around it in its param is still replaced.
func TestScrubCatchesAFragmentOfARehydratedValue(t *testing.T) {
	tagged := map[string]string{"auth": "Bearer <apikey>TOKEN</apikey>", "same": "x", "empty": ""}
	plain := map[string]string{"auth": "Bearer s3cr3t-" + "fragment", "same": "x", "empty": "", "added": "extra-" + "plain"}
	err := scrubError(errors.New("rejected s3cr3t-fragment and extra-plain"), scrubPairs(tagged, plain), nil)
	if containsSubstring(err.Error(), "s3cr3t-fragment") || containsSubstring(err.Error(), "extra-plain") {
		t.Fatalf("scrubbed error still carries plaintext: %v", err)
	}
	if !containsSubstring(err.Error(), "<apikey>TOKEN</apikey>") || !containsSubstring(err.Error(), "[REDACTED]") {
		t.Fatalf("scrubbed error lacks the tag or marker: %v", err)
	}
	if scrubError(nil, nil, nil) != nil {
		t.Fatal("scrubError(nil) is not nil")
	}
}
