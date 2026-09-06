package policy

// Purpose: covers the approval half of the Epic I handler set over a REAL
//   approval queue on a real SQLite store (the approval_queue_test.go
//   fixture, Art.2), and every fail-closed door: an unregistered verb, an
//   elevated verb with nothing to attest it, an unwired collaborator,
//   unreadable params and an id that names nothing.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rpcFixture bundles the real queue with the deps the handlers close over.
type rpcFixture struct {
	*approvalFixture
	deps     RPCDeps
	handlers map[string]MethodFunc
}

// newRPCFixture builds the handler set over the real queue, with an
// attestor enrolled so the elevated verbs are reachable at all.
func newRPCFixture(t *testing.T) *rpcFixture {
	t.Helper()
	f := newApprovalFixture(t)
	deps := RPCDeps{
		Queue:    f.queue,
		Registry: f.reg,
		Grants:   f.grants,
		Clock:    f.clock,
		Attestor: okAttestor{},
	}
	return &rpcFixture{approvalFixture: f, deps: deps, handlers: MethodHandlers(deps)}
}

// call dispatches one method with the given params value.
func (f *rpcFixture) call(t *testing.T, method string, params any) (any, error) {
	t.Helper()
	handler, ok := f.handlers[method]
	if !ok {
		t.Fatalf("no handler is registered for %q", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encoding params: %v", err)
	}
	return handler(context.Background(), raw)
}

// TestMethodHandlersCoverEveryRegisteredVerb proves the handler map and the
// verb registry name the same set: a verb with no handler dispatches
// nowhere, and a handler with no verb can never be authorized.
func TestMethodHandlersCoverEveryRegisteredVerb(t *testing.T) {
	handlers := MethodHandlers(RPCDeps{})
	for _, spec := range RegisteredVerbs() {
		if _, ok := handlers[spec.Method]; !ok {
			t.Errorf("verb %q has no handler", spec.Method)
		}
	}
	for method := range handlers {
		if _, err := LookupVerb(method); err != nil {
			t.Errorf("handler %q is not a registered verb: %v", method, err)
		}
	}
}

// TestEveryHandlerPassesThroughAuthorize proves the guard is on every verb
// and not only the interesting ones: with no attestor enrolled, every
// elevated verb refuses before it touches a collaborator, and it refuses
// even though this fixture's deps are entirely empty.
func TestEveryHandlerPassesThroughAuthorize(t *testing.T) {
	handlers := MethodHandlers(RPCDeps{})
	for _, method := range []string{"approval.grant", "standing_grant.create", "standing_grant.change"} {
		_, err := handlers[method](context.Background(), nil)
		if err == nil {
			t.Errorf("%s succeeded with no attestation source", method)
			continue
		}
		if !cascade.HasKind(err, cascade.KindElevationRequired) {
			t.Errorf("%s refusal kind = %v, want elevation-required", method, err)
		}
	}
}

// TestApprovalListReturnsTheRealQueue drives list and show against entries
// the real queue admitted.
func TestApprovalListReturnsTheRealQueue(t *testing.T) {
	f := newRPCFixture(t)
	res, err := f.call(t, "approval.list", struct{}{})
	if err != nil {
		t.Fatalf("approval.list on a fresh queue: %v", err)
	}
	if listed := res.(ApprovalListResult); len(listed.Pending) != 0 {
		t.Fatalf("a fresh queue listed %d entries, want none", len(listed.Pending))
	}

	enq := f.enqueue(t, "a.txt")
	res, err = f.call(t, "approval.list", struct{}{})
	if err != nil {
		t.Fatalf("approval.list: %v", err)
	}
	listed := res.(ApprovalListResult)
	if len(listed.Pending) != 1 || listed.Pending[0].RequestID != enq.RequestID {
		t.Fatalf("approval.list = %+v, want the one entry %q", listed.Pending, enq.RequestID)
	}

	shown, err := f.call(t, "approval.show", ApprovalShowParams{RequestID: enq.RequestID})
	if err != nil {
		t.Fatalf("approval.show: %v", err)
	}
	if shown.(PendingEntry).RequestID != enq.RequestID {
		t.Errorf("approval.show returned %+v, want request %q", shown, enq.RequestID)
	}
}

// TestApprovalResponsesCarryNoGrantValues asserts the §5.24 structural
// guarantee at the surface: the encoded response of every read verb
// contains no token, nonce or action hash, whatever the queue holds
// internally.
func TestApprovalResponsesCarryNoGrantValues(t *testing.T) {
	f := newRPCFixture(t)
	enq := f.enqueue(t, "a.txt")
	for _, tc := range []struct {
		method string
		params any
	}{
		{"approval.list", struct{}{}},
		{"approval.show", ApprovalShowParams{RequestID: enq.RequestID}},
	} {
		res, err := f.call(t, tc.method, tc.params)
		if err != nil {
			t.Fatalf("%s: %v", tc.method, err)
		}
		encoded, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("encoding the %s response: %v", tc.method, err)
		}
		assertNoGrantFields(t, tc.method, encoded)
	}
}

// assertNoGrantFields walks the decoded JSON tree and fails on any field
// whose NAME is one of the grant values. It checks names rather than
// values, because the guarantee is structural: the field must not exist.
func assertNoGrantFields(t *testing.T, where string, encoded []byte) {
	t.Helper()
	banned := map[string]bool{
		"token": true, "action_hash": true, "nonce": true,
		"access_token": true, "refresh_ref": true, "signed_token": true,
	}
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for key, child := range node {
				if banned[key] {
					t.Errorf("%s response carries the field %q", where, key)
				}
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	var tree any
	if err := json.Unmarshal(encoded, &tree); err != nil {
		t.Fatalf("decoding the %s response: %v", where, err)
	}
	walk(tree)
}

// TestApprovalShowErrorPaths covers the empty id, the unknown id and
// params that are not readable at all.
func TestApprovalShowErrorPaths(t *testing.T) {
	f := newRPCFixture(t)
	if _, err := f.call(t, "approval.show", ApprovalShowParams{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("approval.show with no id = %v, want invalid-input", err)
	}
	if _, err := f.call(t, "approval.show", ApprovalShowParams{RequestID: "nope"}); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("approval.show with an unknown id = %v, want not-found", err)
	}
	_, err := f.handlers["approval.show"](context.Background(), json.RawMessage(`{"request_id":`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("approval.show with unreadable params = %v, want invalid-input", err)
	}
}

// TestApprovalDenyRecordsTheRefusal drives the deny verb through the real
// queue and asserts it reports the state it produced.
func TestApprovalDenyRecordsTheRefusal(t *testing.T) {
	f := newRPCFixture(t)
	enq := f.enqueue(t, "a.txt")
	res, err := f.call(t, "approval.deny", ApprovalDecisionParams{
		RequestID:        enq.RequestID,
		PresentedSummary: enq.Summary,
		PresentedLevel:   L2,
	})
	if err != nil {
		t.Fatalf("approval.deny: %v", err)
	}
	if got := res.(DecisionResult); got.State != "denied" {
		t.Errorf("approval.deny left the entry %s, want denied", got.State)
	}
	if _, err := f.call(t, "approval.deny", ApprovalDecisionParams{}); err == nil {
		t.Error("approval.deny with no request id succeeded")
	}
}

// TestApprovalExpireSweeps drives the expiry verb.
func TestApprovalExpireSweeps(t *testing.T) {
	f := newRPCFixture(t)
	res, err := f.call(t, "approval.expire", struct{}{})
	if err != nil {
		t.Fatalf("approval.expire: %v", err)
	}
	if res.(ApprovalExpireResult).Expired != 0 {
		t.Errorf("a fresh queue expired %d entries, want none", res.(ApprovalExpireResult).Expired)
	}
}

// TestApprovalGrantRejectsForgedToken is R-21.230's named assertion: a
// token that does not verify is refused as permission-denied, and the
// queue state does not change.
func TestApprovalGrantRejectsForgedToken(t *testing.T) {
	f := newRPCFixture(t)
	enq := f.enqueue(t, "a.txt")
	signer := newSignerFixture(t)
	verifier, err := NewApprovalVerifier(signer.public, f.clock)
	if err != nil {
		t.Fatalf("NewApprovalVerifier: %v", err)
	}
	f.deps.Verifier = verifier
	f.handlers = MethodHandlers(f.deps)

	forged := base64.StdEncoding.EncodeToString([]byte("this is not a signed approval record at all"))
	for name, token := range map[string]string{
		"forged":      forged,
		"not base64":  "!!!!not base64!!!!",
		"empty token": "",
	} {
		_, err := f.call(t, "approval.grant", ApprovalGrantParams{
			RequestID:   enq.RequestID,
			SignedToken: token,
			Action:      "a.txt",
		})
		if err == nil {
			t.Fatalf("approval.grant accepted a %s token", name)
		}
		if token != "" && !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Errorf("%s token refusal kind = %v, want permission-denied", name, err)
		}
	}

	pending, err := f.queue.GetPending(context.Background())
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 1 || pending[0].RequestID != enq.RequestID {
		t.Errorf("the queue changed under a refused redemption: %+v", pending)
	}
}

// TestApprovalGrantWithNoVerifierRefuses proves the absent-verifier door:
// with nothing able to tell a real token from an invented one, the verb
// refuses rather than falling back to the request id alone.
func TestApprovalGrantWithNoVerifierRefuses(t *testing.T) {
	f := newRPCFixture(t)
	enq := f.enqueue(t, "a.txt")
	_, err := f.call(t, "approval.grant", ApprovalGrantParams{
		RequestID:   enq.RequestID,
		SignedToken: base64.StdEncoding.EncodeToString([]byte("anything")),
	})
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("approval.grant with no verifier = %v, want permission-denied", err)
	}
}

// TestHandlersRefuseUnwiredCollaborators proves an absent collaborator is
// reported as unavailable rather than replaced by a permissive default.
func TestHandlersRefuseUnwiredCollaborators(t *testing.T) {
	handlers := MethodHandlers(RPCDeps{Attestor: okAttestor{}})
	for _, method := range []string{
		"approval.list", "approval.expire", "standing_grant.list",
		"standing_grant.revoke", "policy.audit_query",
	} {
		if _, err := handlers[method](context.Background(), nil); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s with no collaborator = %v, want unavailable", method, err)
		}
	}
}
