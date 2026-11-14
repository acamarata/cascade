package jobs

// Purpose: a redeemed completion approval token counts only when it was
// filed to run as a human user. SPORT: jobs/completion-gate/ADD.

import (
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// requireHumanPrincipal passes a ConsumeToken refusal through unchanged
// and refuses a redeemed token whose filed subject is not a human user.
func requireHumanPrincipal(res policy.ConsumeResult, err error) error {
	if err != nil {
		return err
	}
	if res.Subject.Kind != policy.SubjectUser {
		return cascade.Wrapf(cascade.KindPermissionDenied, ErrNonHumanApprovalPrincipal,
			"jobs: completion approval principal kind %q is not human", res.Subject.Kind)
	}
	return nil
}
