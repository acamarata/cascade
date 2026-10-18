// Purpose: the fixture helpers every Fleet test shares (jobs, row counts,
// requests, sentinel assertions) and validateDispatch refusing every
// DispatchRef the Fleet cannot lease before any read or write.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestValidateDispatch(t *testing.T) {
	ok := DispatchRef{ProjectID: "p", Requests: 1}
	cases := []struct {
		name    string
		mutate  func(*DispatchRef)
		attempt int
		want    string
	}{
		{"valid", func(*DispatchRef) {}, 1, ""},
		{"no project", func(d *DispatchRef) { d.ProjectID = "" }, 1, "ProjectID is required"},
		{"zero requests", func(d *DispatchRef) { d.Requests = 0 }, 1, "Requests must be >= 1"},
		{"negative tokens", func(d *DispatchRef) { d.HydratedTokens = -1 }, 1, "HydratedTokens must be >= 0"},
		{"attempt zero", func(*DispatchRef) {}, 0, "attempt must be >= 1"},
		{"globs without job", func(d *DispatchRef) { d.RepoID, d.ScopeGlobs = "r", []string{"a"} }, 1, "needs a JobID and a RepoID"},
		{"globs without repo", func(d *DispatchRef) { d.JobID, d.ScopeGlobs = "j", []string{"a"} }, 1, "needs a JobID and a RepoID"},
		{"globs with both", func(d *DispatchRef) { d.JobID, d.RepoID, d.ScopeGlobs = "j", "r", []string{"a"} }, 1, ""},
	}
	for _, c := range cases {
		d := ok
		c.mutate(&d)
		err := validateDispatch(d, c.attempt)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: err = %v, want nil", c.name, err)
			}
			continue
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want KindInvalidInput naming %q", c.name, err, c.want)
		}
	}
}

// seedJob stores a pending job with the given data class.
func (f *fx) seedJob(id string, class provider.SensitivityTier) {
	f.t.Helper()
	j := jobs.Job{ID: id, State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1, Capabilities: []string{"code"}, MutableScope: "repo:x",
		RiskClass: "normal", MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1, Priority: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: &class}
	if err := f.jobs.PutJob(context.Background(), j); err != nil {
		f.t.Fatalf("PutJob: %v", err)
	}
}

// count returns the row count of table.
func (f *fx) count(table string) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		f.t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// zeroRows fails unless the reservation, execution and decision tables are empty.
func (f *fx) zeroRows() {
	f.t.Helper()
	for _, table := range []string{"jobs_reservation", "jobs_execution", "test_decision"} {
		if n := f.count(table); n != 0 {
			f.t.Errorf("%s has %d rows, want 0", table, n)
		}
	}
}

// request is a valid internal-sensitivity request.
func request() ResourceRequest {
	return ResourceRequest{Intent: "build", TaskClass: conductor.TaskClassCode, MinQuality: QualityStandard, Deadline: DeadlineInteractive,
		Sensitivity: provider.SensitivityInternal, RiskClass: jobs.RiskClassNormal}
}

// dispatch is a job-backed dispatch reference.
func dispatchFor(job string) DispatchRef {
	return DispatchRef{JobID: job, ProjectID: "project-1", HydratedTokens: 1000, Requests: 1}
}

// requireSentinel fails unless err carries sentinel by identity and names every want.
func requireSentinel(t *testing.T, err, sentinel error, wants ...string) {
	t.Helper()
	if !hasSentinel(err, sentinel) {
		t.Fatalf("err = %v, want the %q sentinel by identity", err, sentinel)
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("err = %q, want it to name %q", err.Error(), w)
		}
	}
}
