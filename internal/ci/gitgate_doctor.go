package ci

// Purpose: the doctor check that warns while mirror pushes are pending.
// Inputs: an injected source of unconfirmed integration-site outbox rows.
// Outputs: WARN with the backlog count while it is above zero, else OK.
// Constraints: the integration outbox site is shared with job advance and
// scheduler cancel, so only rows whose job id or idempotency key carries
// MirrorEffectPrefix count. The check only reads.
// SPORT: internal.ci.MirrorBacklogCheck/ADDED.

import (
	"context"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/jobs"
)

// MirrorBacklogCount counts the mirror rows among unconfirmed outbox rows.
func MirrorBacklogCount(rows []jobs.OutboxRow) int {
	n := 0
	for _, r := range rows {
		if strings.HasPrefix(r.JobID, MirrorEffectPrefix) || strings.Contains(r.IdempotencyKey, ":"+MirrorEffectPrefix) {
			n++
		}
	}
	return n
}

// MirrorBacklogCheck is the "ci_mirror_backlog" doctor check.
type MirrorBacklogCheck struct {
	// Unconfirmed lists integration-site rows still in intent or effect.
	Unconfirmed func(ctx context.Context) ([]jobs.OutboxRow, error)
}

// Name implements doctor.Check.
func (MirrorBacklogCheck) Name() string { return "ci_mirror_backlog" }

// Describe implements doctor.Check.
func (MirrorBacklogCheck) Describe() string {
	return "warns while accepted branches wait to be mirrored to the configured remote"
}

// Metadata implements doctor.Check.
func (MirrorBacklogCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

// Run implements doctor.Check.
func (c MirrorBacklogCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.Unconfirmed == nil {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "mirror backlog not tracked on this host"}, nil
	}
	rows, err := c.Unconfirmed(ctx)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "mirror backlog unreadable", Detail: err.Error()}, nil
	}
	n := MirrorBacklogCount(rows)
	if n == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "no mirror push is pending"}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusWarn, Message: strconv.Itoa(n) + " mirror push(es) pending",
		Remediation: "check the remote and the host git credential helper; pending pushes retry on the next accepted branch"}, nil
}

// Fix implements doctor.Check; a backlog is cleared by the push, not here.
func (MirrorBacklogCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
