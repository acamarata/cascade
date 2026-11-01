package jobs

import (
	"context"
	"math"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestJobSpecHashColumns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	base := baseJob("job-spec")
	if err := st.PutJob(ctx, base); err != nil {
		t.Fatal(err)
	}
	for _, dep := range []string{"dep-b", "dep-a"} {
		if err := st.PutDependency(ctx, TaskDependency{JobID: base.ID, DependsOnJobID: dep}); err != nil {
			t.Fatal(err)
		}
	}
	hashOf := func(j Job) string {
		deps, err := st.Dependencies(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		return JobSpecHash(j, deps)
	}
	stored, _, _ := st.GetJob(ctx, base.ID)
	want := hashOf(stored)
	if len(want) != 64 || JobSpecHash(stored, []string{"dep-a", "dep-b"}) != want {
		t.Fatalf("JobSpecHash = %q, want a 64-hex digest independent of edge order", want)
	}
	assertSpecHashColumns(t, stored, want)
	if err := st.PutDependency(ctx, TaskDependency{JobID: base.ID, DependsOnJobID: "dep-c"}); err != nil {
		t.Fatal(err)
	}
	if hashOf(stored) == want {
		t.Fatal("a new jobs_task_dependency edge left JobSpecHash unchanged")
	}
}

// assertSpecHashColumns: every hashed column moves the digest; state and
// bookkeeping columns do not.
func assertSpecHashColumns(t *testing.T, stored Job, want string) {
	t.Helper()
	changes := map[string]func(*Job){
		"id": func(j *Job) { j.ID += "x" }, "capabilities": func(j *Job) { j.Capabilities = append(j.Capabilities, "gpu") },
		"mutable_scope": func(j *Job) { j.MutableScope += "/x" }, "risk_class": func(j *Job) { j.RiskClass += "x" },
		"min_task_class": func(j *Job) { j.MinTaskClass += "x" }, "node_requirements": func(j *Job) { j.NodeRequirements = `{"os":"linux"}` },
		"timeout_seconds": func(j *Job) { j.TimeoutSeconds++ }, "cost_ceiling": func(j *Job) { j.CostCeiling += 0.5 },
		"priority": func(j *Job) { j.Priority++ }, "consequence_class": func(j *Job) { j.ConsequenceClass = ConsequenceClass("other") },
		"data_class": func(j *Job) { j.DataClass = new(provider.SensitivityLocalOnly) },
	}
	for name, change := range changes {
		j := stored
		j.Capabilities = append([]string(nil), stored.Capabilities...)
		change(&j)
		if JobSpecHash(j, []string{"dep-a", "dep-b"}) == want {
			t.Errorf("changing %s left JobSpecHash unchanged", name)
		}
	}
	unchanged := map[string]func(*Job){
		"state": func(j *Job) { j.State = JobState("running") }, "created_at": func(j *Job) { j.CreatedAt += 100 },
		"updated_at": func(j *Job) { j.UpdatedAt += 100 }, "consecutive_failed_attempts": func(j *Job) { j.ConsecutiveFailedAttempts = 3 },
	}
	for name, change := range unchanged {
		j := stored
		change(&j)
		if JobSpecHash(j, []string{"dep-a", "dep-b"}) != want {
			t.Errorf("changing %s changed JobSpecHash", name)
		}
	}
}

func TestJobSpecHashRefusesNonCanonicalSpecs(t *testing.T) {
	nan := baseJob("job-nan")
	nan.CostCeiling = math.NaN()
	badClass := baseJob("job-class")
	badClass.DataClass = new(provider.SensitivityTier(250))
	for name, j := range map[string]Job{"NaN cost_ceiling": nan, "unknown data_class": badClass} {
		if got := JobSpecHash(j, nil); got != "" {
			t.Errorf("JobSpecHash(%s) = %q, want empty (no canonical form)", name, got)
		}
	}
	if JobSpecHash(baseJob("job-ok"), nil) == "" {
		t.Fatal("JobSpecHash(valid job) is empty")
	}
}
