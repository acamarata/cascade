package ci

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/jobs"
)

func TestMirrorBacklogIgnoresOtherIntegrationRows(t *testing.T) {
	other := jobs.OutboxRow{JobID: "job-1", IdempotencyKey: jobs.DeriveIdempotencyKey(jobs.OutboxSiteIntegration, "job-1", 0, "h")}
	cancel := jobs.OutboxRow{JobID: "job-2", IdempotencyKey: jobs.DeriveIdempotencyKey(jobs.OutboxSiteIntegration, "job-2", 3, "c")}
	mirror := jobs.OutboxRow{JobID: MirrorEffectPrefix + "r:b:" + mirrorTip,
		IdempotencyKey: jobs.DeriveIdempotencyKey(jobs.OutboxSiteIntegration, MirrorEffectPrefix+"r:b:"+mirrorTip, 0, "p")}
	run := func(rows []jobs.OutboxRow, err error) doctor.CheckResult {
		res, rerr := MirrorBacklogCheck{Unconfirmed: func(context.Context) ([]jobs.OutboxRow, error) { return rows, err }}.Run(context.Background())
		if rerr != nil {
			t.Fatal(rerr)
		}
		return res
	}
	if got := run([]jobs.OutboxRow{other, cancel}, nil); got.Status != doctor.StatusOK {
		t.Fatalf("other integration rows gave %+v, want OK", got)
	}
	if got := run([]jobs.OutboxRow{other, mirror, cancel}, nil); got.Status != doctor.StatusWarn || MirrorBacklogCount([]jobs.OutboxRow{other, mirror, cancel}) != 1 {
		t.Fatalf("one mirror row gave %+v, want WARN with count 1", got)
	}
	if got := run(nil, errors.New("db down")); got.Status != doctor.StatusError {
		t.Fatalf("unreadable backlog gave %+v, want error", got)
	}
}
