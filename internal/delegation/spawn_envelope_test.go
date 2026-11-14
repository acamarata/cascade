// Purpose: the spawn path projects the signed child record through
// agents.ProjectEnvelope, refuses a record that does not verify, and carries a
// mutation's repo root and scope into the spawned job.

package delegation

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/agents"
)

func TestSpawnProjectsSignedEnvelope(t *testing.T) {
	ctx := context.Background()
	c, good, svc, f, sh := setup(t)
	resp, err := svc.Delegate(ctx, &Request{AgentID: "a", Parent: good})
	failIf(t, err != nil || resp == nil || len(sh.reqs) != 1, "Delegate = %+v, %v with %d spawns; want one spawn", resp, err, len(sh.reqs))
	want, err := agents.ProjectEnvelope(resp.Record, c.s.PublicKey(), t0)
	got := sh.reqs[0].Job.Envelope
	failIf(t, err != nil || !reflect.DeepEqual(got, want) || got.Principal != resp.Record.Env.Principal || len(got.Roots) == 0,
		"spawned envelope = %+v, want the projection %+v (%v)", got, want, err)
	failIf(t, strings.Join(f.calls, ",") != "commit", "fleet calls %v; want commit", f.calls)

	// The child is signed and verified at t0, but it has expired by the time it
	// reaches the spawn: the projection refuses it and nothing is spawned.
	clock := &steppedClock{now: t0}
	forged, f2, sh2 := c.service(t0)
	forged.deps.Clock = clock
	forged.deps.RepoIDOf = func(context.Context, string) (string, error) { clock.now = t0.Add(48 * time.Hour); return "", nil }
	resp, err = forged.Delegate(ctx, &Request{AgentID: "b", JobID: "j1", Mutation: true, Parent: good})
	failIf(t, err == nil || resp != nil || len(sh2.reqs) != 0, "unverifiable child: Delegate = %+v, %v with %d spawns; want a refusal and no spawn", resp, err, len(sh2.reqs))
	failIf(t, strings.Join(f2.calls, ",") != "abort", "unverifiable child: fleet calls %v; want abort", f2.calls)
	wantRefused(t, c, "b", err)
}

// steppedClock is a runtime.Clock a test moves forward.
type steppedClock struct{ now time.Time }

func (c *steppedClock) Now() time.Time { return c.now }

func TestMutationDelegationResolvesRepoRoot(t *testing.T) {
	ctx := context.Background()
	c, good, svc, f, sh := setup(t)
	root := filepath.Join(t.TempDir(), "job-wt")
	var asked []string
	svc.deps.RepoIDOf = func(_ context.Context, r string) (string, error) { asked = append(asked, r); return "repo-a", nil }
	scope := []string{"cmd", "internal/x/"}
	resp, err := svc.Delegate(ctx, &Request{AgentID: "m", JobID: "j1", Mutation: true, RepoRoot: root, InScope: scope, Parent: good})
	failIf(t, err != nil || resp == nil || len(sh.reqs) != 1, "Delegate = %+v, %v with %d spawns; want one spawn", resp, err, len(sh.reqs))
	job := sh.reqs[0].Job
	failIf(t, job.WorktreeRoot != root || !slices.Equal(job.Scope.InScopePrefixes, scope) || job.ToolPolicy != "all",
		"spawned job root %q scope %v policy %q; want %q %v all", job.WorktreeRoot, job.Scope.InScopePrefixes, job.ToolPolicy, root, scope)
	failIf(t, !slices.Equal(asked, []string{root}) || len(f.refs) != 1 || f.refs[0].RepoID != "repo-a",
		"RepoIDOf asked %v, leased %+v; want the repo root once and RepoID repo-a", asked, f.refs)
	failIf(t, !slices.Equal(resp.InScopePrefixes, scope), "InScopePrefixes = %v; want %v", resp.InScopePrefixes, scope)
	rec, err := c.st.GetDelegation(ctx, resp.Record.Env.ID)
	failIf(t, err != nil || !rec.Mutation || rec.JobID != "j1" || rec.State != jobs.DelegationLive, "record = %+v, %v; want a live mutation of j1", rec, err)
}
