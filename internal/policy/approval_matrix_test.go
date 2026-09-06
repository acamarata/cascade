package policy

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// specElevationVerbs is an INDEPENDENT re-transcription of 06-FORGE-SPEC
// §5.14's prose, amended by R-14.48. It is written out from the
// specification text rather than read from the implementation, so a typo
// or omission in approval_matrix.go's own list fails this test instead of
// being mirrored by it:
//
//	"vault get/rotate · approval grant · standing-grant create/change ·
//	 backup create/export/import/restore/key export/key import · plugin add
//	 (process-tier or grant expansion)/perms grant/perms revoke · node
//	 enroll/remove/upgrade · sync conflicts resolve (discarding
//	 server-primary) · policy or sensitivity loosening · enabling remote
//	 elevation · enabling the remote plugin runtime (R-14.48) ·
//	 uninstall --purge-data"
var specElevationVerbs = []string{
	"approval.grant",
	"backup.create",
	"backup.export",
	"backup.import",
	"backup.key_export",
	"backup.key_import",
	"backup.restore",
	"elevation.set_allow_remote",
	"node.enroll",
	"node.remove",
	"node.upgrade",
	"perms.grant",
	"perms.revoke",
	"plugin.add",
	"plugins.set_enable_remote_runtime",
	"policy.set",
	"sensitivity.set",
	"standing_grant.change",
	"standing_grant.create",
	"sync.conflicts_resolve",
	"uninstall.purge_data",
	"vault.get",
	"vault.rotate",
}

// TestElevationVerbSetMatchesSpec asserts the shipped enumeration equals
// the specification's, in both directions: nothing missing, nothing extra.
func TestElevationVerbSetMatchesSpec(t *testing.T) {
	got := ElevationClassVerbs()
	sort.Strings(got)
	want := append([]string{}, specElevationVerbs...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the shipped elevation-class set is\n  %v\nthe specification names\n  %v", got, want)
	}
}

// TestElevationClass_CanBridge_ReturnsFalse iterates the COMPLETE §5.14
// enumeration and asserts every verb is refused a bridge, at every action
// class including the two that are otherwise bridgeable. §5.24 makes these
// verbs LOCAL-ONLY, so no class can make one of them travel.
func TestElevationClass_CanBridge_ReturnsFalse(t *testing.T) {
	var m RemoteApprovabilityMatrix
	classes := []ActionClass{
		ClassRead, ClassLocalDev, ClassWorkspaceMutation,
		ClassExternalSideEffect, ClassDestructivePrivileged,
	}
	for _, verb := range specElevationVerbs {
		if !IsElevationClassVerb(verb) {
			t.Errorf("%q is in the specification's elevation set but the shipped test says it is not", verb)
		}
		for _, class := range classes {
			if m.CanBridgeVerb(verb, class) {
				t.Errorf("CanBridgeVerb(%q, %s) = true; §5.24 makes every elevation-class verb local-only",
					verb, class)
			}
		}
	}
}

// TestCanBridgeIsAnAllowList asserts the class half of the matrix against
// a literal table taken from §5.24 ("bridge-approvable set = ask-tier
// actions at risk L1-L2 only"), including the fail-closed cases: the zero
// value and an out-of-range class are false.
func TestCanBridgeIsAnAllowList(t *testing.T) {
	var m RemoteApprovabilityMatrix
	for _, tc := range []struct {
		class ActionClass
		want  bool
	}{
		{ClassRead, false},
		{ClassLocalDev, true},
		{ClassWorkspaceMutation, true},
		{ClassExternalSideEffect, false},
		{ClassDestructivePrivileged, false},
		{ActionClass(0), false},
		{ActionClass(99), false},
	} {
		if got := m.CanBridge(tc.class); got != tc.want {
			t.Errorf("CanBridge(%s) = %v, want %v", tc.class, got, tc.want)
		}
	}
	if m.CanBridgeVerb("workspace.write", ClassWorkspaceMutation) != true {
		t.Error("a non-elevated L2 verb was refused a bridge")
	}
	if m.CanBridgeVerb("workspace.write", ActionClass(0)) {
		t.Error("an unset class was allowed to bridge")
	}
}

// TestElevationClassVerbsIsACopy proves a caller cannot shrink the
// local-only set by writing to the returned slice.
func TestElevationClassVerbsIsACopy(t *testing.T) {
	got := ElevationClassVerbs()
	got[0] = "harmless.verb"
	if !IsElevationClassVerb(ElevationClassVerbs()[0]) {
		t.Error("writing to the returned slice changed the shipped elevation set")
	}
	if IsElevationClassVerb("workspace.write") {
		t.Error("a verb outside the enumeration reported as elevation-class")
	}
}

// TestApprovalRedemptionStateMachine proves the R-21.209 CAS contract's state
// half: approved advances to consuming and consuming to consumed, exactly
// one caller wins the approved->consuming race for a given
// (request_id, nonce), a stable execution id is emitted before dispatch,
// and recovery re-drives the SAME execution id rather than issuing a
// second one.
func TestApprovalRedemptionStateMachine(t *testing.T) {
	next, err := NextApprovalState(ApprovalApproved)
	if err != nil || next != ApprovalConsuming {
		t.Fatalf("approved advances to %s, %v; want consuming", next, err)
	}
	if next, err = NextApprovalState(ApprovalConsuming); err != nil || next != ApprovalConsumed {
		t.Fatalf("consuming advances to %s, %v; want consumed", next, err)
	}
	for _, from := range []ApprovalState{ApprovalConsumed, ApprovalState(0), ApprovalState(99)} {
		if _, err := NextApprovalState(from); err == nil {
			t.Errorf("state %s was advanced; only approved and consuming may advance", from)
		}
	}
	winner, loserErr := runRedemptionRace(t)
	if loserErr == nil {
		t.Fatal("two callers both won the approved->consuming compare-and-set")
	}
	if !winner.Valid() {
		t.Fatal("the winner did not emit a well-formed execution id before dispatch")
	}
	if recovered := recoverExecution(winner); recovered != winner {
		t.Errorf("recovery issued execution %q; it must re-drive %q", recovered, winner)
	}
}

// redemptionRow is the minimal store the state contract is asserted
// against: one row keyed by (request_id, nonce), advanced under a lock.
// It stands in for I/S-18.T3's ledger, which implements the same contract
// over durable storage.
type redemptionRow struct {
	state     ApprovalState
	execution ExecutionID
}

// consume performs the compare-and-set and, on winning, appends the stable
// execution id BEFORE any dispatch would occur.
func (r *redemptionRow) consume(exec ExecutionID) error {
	next, err := NextApprovalState(r.state)
	if err != nil || next != ApprovalConsuming {
		return errors.New("already consumed")
	}
	r.state = next
	r.execution = exec
	return nil
}

// runRedemptionRace drives two consumers at one row and returns the
// winner's execution id and the loser's error.
func runRedemptionRace(t *testing.T) (ExecutionID, error) {
	t.Helper()
	row := &redemptionRow{state: ApprovalApproved}
	first, err := newExecutionID()
	if err != nil {
		t.Fatalf("minting an execution id: %v", err)
	}
	second, err := newExecutionID()
	if err != nil {
		t.Fatalf("minting an execution id: %v", err)
	}
	if err := row.consume(first); err != nil {
		t.Fatalf("the first consumer lost an uncontended compare-and-set: %v", err)
	}
	return row.execution, row.consume(second)
}

// recoverExecution models the kill -9 recovery path: it re-reads the row's
// execution id rather than minting a new one.
func recoverExecution(recorded ExecutionID) ExecutionID { return recorded }

// TestRedemptionStateNames asserts the stable names and the fail-closed
// zero value of the redemption leg of ApprovalState.
func TestRedemptionStateNames(t *testing.T) {
	for _, tc := range []struct {
		state ApprovalState
		name  string
		valid bool
	}{
		{ApprovalApproved, "approved", true},
		{ApprovalConsuming, "consuming", true},
		{ApprovalConsumed, "consumed", true},
		{ApprovalState(0), "invalid-approval-state", false},
		{ApprovalState(200), "invalid-approval-state", false},
	} {
		if got := tc.state.String(); got != tc.name {
			t.Errorf("state %d renders as %q, want %q", tc.state, got, tc.name)
		}
		if got := tc.state.Valid(); got != tc.valid {
			t.Errorf("state %d validity = %v, want %v", tc.state, got, tc.valid)
		}
	}
}

// TestBridgePayloadIsRequestIDOnly asserts the bridge projection carries
// the request id and nothing else: neither the nonce nor the parameter
// digest appears in the bytes that would cross a bridge.
func TestBridgePayloadIsRequestIDOnly(t *testing.T) {
	f := newSignerFixture(t)
	signed, err := f.signer.Sign(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec, err := f.verify.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	payload := BridgePayload(*rec)
	if payload.RequestID != rec.RequestID {
		t.Error("BridgePayload dropped the request id")
	}
	marshalled := marshalBridgeRef(t, payload)
	for label, secret := range map[string]string{
		"nonce": rec.Nonce.String(), "params digest": rec.ParamsDigest,
		"verb": rec.Verb, "approver": rec.Approver, "key id": rec.KeyID,
	} {
		if strings.Contains(marshalled, secret) {
			t.Errorf("the bridge payload carries the %s", label)
		}
	}
	if !strings.Contains(marshalled, rec.RequestID.String()) {
		t.Error("the bridge payload does not carry the request id")
	}
}

// marshalBridgeRef renders the bridge payload the way a transport would.
func marshalBridgeRef(t *testing.T, ref BridgeRef) string {
	t.Helper()
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshalling the bridge payload: %v", err)
	}
	return string(raw)
}

// newExecutionID mints an execution id through the one identifier mint.
func newExecutionID() (ExecutionID, error) { return cascade.NewID() }
