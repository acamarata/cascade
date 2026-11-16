// Package policy (owner_audit_test.go): the P2-GOV-85 acceptance tests.
//
// One L2 decision widened by a live owner grant must append exactly one
// audit record naming the grant id, the subject and the action; an L0/L1
// decision the base profile already allowed appends none; and a writer
// that fails keeps the base verdict (ask), never allow.
// SPORT: internal.policy.auditOwnerGrant/ADDED (P2-GOV-85).

package policy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// captureWriter records every event handed to Append. It is the offline
// stand-in for *audit.Log: no store, no clock, no randomness.
type captureWriter struct {
	events []audit.Event
	err    error
}

func (w *captureWriter) Append(ctx context.Context, e audit.Event) (audit.Record, error) {
	if w.err != nil {
		return audit.Record{}, w.err
	}
	w.events = append(w.events, e)
	return audit.Record{Seq: uint64(len(w.events)), Event: e}, nil
}

// grantExplain decodes the rationale body of a grant-decision record.
func grantExplain(t *testing.T, e audit.Event) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(e.Explain, &m); err != nil {
		t.Fatalf("explain is not JSON: %v", err)
	}
	return m
}

// TestGrantDecisionAuditedWithGrantID proves one L2 decision under a live
// record appends exactly one record naming the grant id, subject, action
// and rung.
func TestGrantDecisionAuditedWithGrantID(t *testing.T) {
	w := &captureWriter{}
	subject := Subject{Kind: SubjectAgent, ID: "agent-one"}
	grantID := cascade.ID("grant-under-test")

	if err := auditOwnerGrant(context.Background(), w, grantID, subject, "repo.write", L2.String()); err != nil {
		t.Fatalf("auditOwnerGrant = %v, want nil", err)
	}
	if len(w.events) != 1 {
		t.Fatalf("appended %d records, want exactly one", len(w.events))
	}
	e := w.events[0]
	body := grantExplain(t, e)
	if body["grant_id"] != string(grantID) {
		t.Errorf("grant_id = %q, want %q", body["grant_id"], grantID)
	}
	if !strings.Contains(body["subject"], subject.ID) || !strings.Contains(e.Actor, subject.ID) {
		t.Errorf("record does not name the subject %q: %+v", subject.ID, e)
	}
	if e.Action != "repo.write" || body["action"] != "repo.write" {
		t.Errorf("record does not name the action: %+v", e)
	}
	if body["rung"] != L2.String() || e.RiskLevel != L2.String() {
		t.Errorf("record does not name the rung %s: %+v", L2, e)
	}
}

// TestBaseAllowWritesNoGrantRecord proves an L0 or L1 decision the base
// profile already allowed writes no grant record even under a live grant.
func TestBaseAllowWritesNoGrantRecord(t *testing.T) {
	for _, lvl := range []RiskLevel{L0, L1} {
		w := &captureWriter{}
		verdict := consultOwnerGrant(context.Background(), w,
			cascade.ID("grant-under-test"), Subject{Kind: SubjectAgent, ID: "agent-one"},
			"repo.read", lvl, VerdictAllow)
		if verdict != VerdictAllow {
			t.Errorf("%s verdict = %s, want allow", lvl, verdict)
		}
		if len(w.events) != 0 {
			t.Errorf("%s appended %d grant records, want none", lvl, len(w.events))
		}
	}
}

// TestAuditWriteFailureKeepsBaseVerdict proves a failing writer makes the
// widened L2 decision ask again rather than allow unaudited.
func TestAuditWriteFailureKeepsBaseVerdict(t *testing.T) {
	boom := errors.New("audit store down")
	w := &captureWriter{err: boom}
	verdict := consultOwnerGrant(context.Background(), w,
		cascade.ID("grant-under-test"), Subject{Kind: SubjectAgent, ID: "agent-one"},
		"repo.write", L2, VerdictAsk)
	if verdict != VerdictAsk {
		t.Fatalf("verdict = %s, want ask when the audit write fails", verdict)
	}
	if len(w.events) != 0 {
		t.Errorf("a failed write left %d records behind", len(w.events))
	}
}
