// Package policy (owner_audit.go): Purpose: the audit half of the owner
//
//	consult step (P2-GOV-85). Every decision the autonomous table turned
//	from ask into allow under a live owner grant appends exactly one
//	audit record naming the grant id, the subject, the action and the
//	rung. A decision the base profile already allowed writes no such
//	record: there is no grant to account for.
// Inputs: an injected audit.Writer, the resolved grant id, the deciding
//	Subject, the action name and the rung as detail.
// Outputs: nil once the record is sealed, or the writer's error unchanged.
// Constraints: no clock read and no randomness here — the log stamps both;
//	no payload ever reaches the record, only names and the grant id.
// SPORT: internal.policy.auditOwnerGrant/ADDED (P2-GOV-85).

package policy

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ownerGrantExplain is the rationale body of a grant-decision record: the
// grant that authorised the widening, who decided, what was attempted and
// at which rung. It carries names only, never parameters.
type ownerGrantExplain struct {
	GrantID string `json:"grant_id"`
	Subject string `json:"subject"`
	Action  string `json:"action"`
	Rung    string `json:"rung"`
}

// auditOwnerGrant appends the one record that accounts for a decision
// widened from ask to allow by a live owner grant. grantID is the redeemed
// grant's id, subject the deciding principal, action the attempted verb and
// detail the rung it was classified at. The writer's error is returned
// unchanged so the caller can fall back to the base verdict.
func auditOwnerGrant(ctx context.Context, w audit.Writer, grantID cascade.ID, subject Subject, action string, detail string) error {
	if w == nil {
		return cascade.New(cascade.KindInvalidInput, "policy: owner grant audit needs a writer")
	}
	explain, err := json.Marshal(ownerGrantExplain{
		GrantID: string(grantID),
		Subject: subject.String(),
		Action:  action,
		Rung:    detail,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "policy: encoding owner grant explain")
	}
	if _, err := w.Append(ctx, audit.Event{
		Kind:      audit.KindApprovalGrant,
		Actor:     subject.String(),
		Action:    action,
		RiskLevel: detail,
		Verdict:   string(VerdictAllow),
		Explain:   explain,
		Outcome:   "allowed-under-owner-grant",
	}); err != nil {
		return err
	}
	return nil
}
