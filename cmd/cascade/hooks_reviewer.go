//go:build !windows

// Purpose: compose the reviewer enforcement into the daemon completion gate.
//
//	The policy is review.JobReviewPolicy, the families come from
//	reviewerInputs over the daemon's Fleet, jobs store and provider
//	registry reader, and decisions land in the daemon jobs store.
//
// Inputs: the composed FleetRuntime and the provider registry reader the
// daemon already holds (w.Deps); no second handle is opened here.
// Outputs: the *jobs.ReviewerEnforcement the gate's policy carries, and the
// reviewer tree a review job's accepted edge is bound to.
// Constraints: no family rule or ladder lives here; any lookup or tree error
// denies (fail closed) and never reads as an unresolved family.
//
// SPORT: cmd/cascade/reviewer (ADD, P1-PBD-05).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/review"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewerSources are the daemon values the reviewer enforcement reads.
type reviewerSources struct {
	fleet    *daemon.FleetRuntime
	registry provider.ProviderRegistryReader
}

// reviewerSourcesOf reads the optional trailing gate arguments: the
// FleetRuntime and the provider registry reader. Absent ones stay nil.
func reviewerSourcesOf(extra []any) reviewerSources {
	var src reviewerSources
	if len(extra) > 0 {
		src.fleet, _ = extra[0].(*daemon.FleetRuntime)
	}
	if len(extra) > 1 {
		src.registry, _ = extra[1].(provider.ProviderRegistryReader)
	}
	return src
}

// composeReviewer applies the reviewer schema on the Fleet's jobs handle and
// builds the enforcement. With no Fleet it returns nil, so the policy denies
// every review job as not configured.
func composeReviewer(ctx context.Context, clock migrate.Clock, src reviewerSources) (*jobs.ReviewerEnforcement, error) {
	if src.fleet == nil || src.fleet.JobsDB() == nil {
		return nil, nil
	}
	if err := jobs.ApplyReviewerSchema(ctx, src.fleet.JobsDB(), migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return nil, err
	}
	return newReviewerEnforcement(src.fleet, src.registry), nil
}

// newReviewerEnforcement builds the enforcement over rt and reg. With no
// registry reader or no Fleet the Inputs stay nil and the policy denies.
func newReviewerEnforcement(rt *daemon.FleetRuntime, reg provider.ProviderRegistryReader) *jobs.ReviewerEnforcement {
	if rt == nil {
		return nil
	}
	en := &jobs.ReviewerEnforcement{Policy: jobReviewPolicy, Decisions: rt.Jobs()}
	if reg == nil || rt.Fleet() == nil {
		return en
	}
	en.Inputs = reviewerInputsByID{in: reviewerInputs{
		jobs: rt.Jobs(), jobsDB: rt.JobsDB(), fleet: rt.Fleet(), lanes: registryLanes(reg), reg: reg,
	}}
	return en
}

// jobReviewPolicy is review.JobReviewPolicy for a derived risk class.
func jobReviewPolicy(rc jobs.RiskClass) (jobs.ReviewPolicySource, error) {
	p, err := review.NewJobReviewPolicy(rc)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// registryLanes lists the reader's lanes as the records reviewerInputs
// matches on (lane name and provider name).
func registryLanes(reg provider.ProviderRegistryReader) func(context.Context) ([]registry.LaneRecord, error) {
	return func(ctx context.Context) ([]registry.LaneRecord, error) {
		lanes, err := reg.ListLanes(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]registry.LaneRecord, 0, len(lanes))
		for _, l := range lanes {
			out = append(out, registry.LaneRecord{LaneName: l.LaneName, ProviderName: l.ProviderName})
		}
		return out, nil
	}
}

// reviewerInputsByID adapts reviewerInputs to jobs.ReviewerInputs, which
// names the review job by id.
type reviewerInputsByID struct{ in reviewerInputs }

// AuthorFamilies implements jobs.ReviewerInputs.
func (a reviewerInputsByID) AuthorFamilies(ctx context.Context, reviewJobID string) ([]jobs.FamilyRef, error) {
	job, err := a.job(ctx, reviewJobID)
	if err != nil {
		return nil, err
	}
	fams, err := a.in.AuthorFamilies(ctx, job)
	if err != nil {
		return nil, err
	}
	out := make([]jobs.FamilyRef, 0, len(fams))
	for _, f := range fams {
		out = append(out, jobs.FamilyRef{Family: f.Family, Resolved: f.Resolved})
	}
	return out, nil
}

// ReviewerFamily implements jobs.ReviewerInputs.
func (a reviewerInputsByID) ReviewerFamily(ctx context.Context, reviewJobID string) (jobs.FamilyRef, error) {
	job, err := a.job(ctx, reviewJobID)
	if err != nil {
		return jobs.FamilyRef{}, err
	}
	f, err := a.in.ReviewerFamily(ctx, job)
	if err != nil {
		return jobs.FamilyRef{}, err
	}
	return jobs.FamilyRef{Family: f.Family, Resolved: f.Resolved}, nil
}

// job reads id; a missing job is an error, never an unresolved family.
func (a reviewerInputsByID) job(ctx context.Context, id string) (jobs.Job, error) {
	job, found, err := a.in.jobs.GetJob(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	if !found {
		return jobs.Job{}, cascade.Newf(cascade.KindNotFound, "reviewer: unknown job %q", id)
	}
	return job, nil
}

// reviewerTreeFor returns the clean tree of job's one held lease worktree,
// fenced by the lease manager the Fleet's worktree manager was built with.
// No held lease, more than one, or a dirty worktree is an error.
func reviewerTreeFor(ctx context.Context, rt *daemon.FleetRuntime, job jobs.Job) (string, error) {
	if rt == nil || rt.Worktrees() == nil {
		return "", cascade.New(cascade.KindUnavailable, "reviewer: no fleet worktree manager")
	}
	return rt.Worktrees().ReviewerTreeForJob(ctx, job.ID)
}

// treeSource is the gate's reviewer tree reader over src's Fleet.
func (src reviewerSources) treeSource() func(context.Context, jobs.Job) (string, error) {
	return func(ctx context.Context, job jobs.Job) (string, error) {
		return reviewerTreeFor(ctx, src.fleet, job)
	}
}

// fillReviewerTree binds a review job's accepted edge to its worktree tree.
// It returns a denial reason when the tree cannot be read; then the caller
// must not call Transition.
func (g *completionPolicyGate) fillReviewerTree(ctx context.Context, job jobs.Job, req *jobs.TransitionRequest) string {
	if job.MinTaskClass != string(conductor.TaskClassReview) {
		return ""
	}
	if g.reviewerTree == nil {
		return "reviewer worktree unreadable: no fleet worktree manager"
	}
	tree, err := g.reviewerTree(ctx, job)
	if err != nil {
		return "reviewer worktree unreadable: " + err.Error()
	}
	req.ReviewerTreeAtCompletion = tree
	return ""
}

// reviewerDenial appends the reviewer decision row a review job's denial
// produced, as the evidence reference that cites it.
func (g *completionPolicyGate) reviewerDenial(ctx context.Context, job jobs.Job, reason string) string {
	if job.MinTaskClass != string(conductor.TaskClassReview) {
		return reason
	}
	rows, err := g.store.ReviewerDecisions(ctx, job.ID)
	if err != nil {
		return reason + " (reviewer decision unreadable: " + err.Error() + ")"
	}
	if len(rows) == 0 {
		return reason
	}
	return reason + " (" + jobs.ReviewEvidenceRef(rows[len(rows)-1]) + ")"
}
