//go:build !windows

// Purpose: the daemon's reviewer inputs: the provider family a job ran on,
//
//	read from the job's latest execution, the reservation the Fleet bound
//	to it, that reservation's registry lane and the lane provider's driver.
//
// Inputs: the one jobs handle, the Fleet's reservation lookup, the registry
// lane list and the provider registry reader.
// Outputs: a reviewerFamily per job; Resolved false when the chain has no
// answer, and any read error returned as that error (never as unresolved).
// Constraints: no family rule or ladder lives here; the family is the
// registry driver of the reservation lane's provider, so two accounts of one
// provider are one family.
//
// SPORT: cmd/cascade/reviewer (ADD, P1-PBD-05).
package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewerFamily is one job's resolved provider family. Resolved false
// means the chain proved nothing, which is never treated as distinct.
type reviewerFamily struct {
	Family   string
	Resolved bool
}

// reservationLookup is the Fleet's reservation read (*capacity.Fleet).
type reservationLookup interface {
	ReservationByExecution(ctx context.Context, executionID string) (economics.Reservation, bool, error)
}

// reviewerInputs resolves job families over the daemon's runtime.
type reviewerInputs struct {
	jobs   *jobs.Store
	jobsDB *sql.DB
	fleet  reservationLookup
	lanes  func(context.Context) ([]registry.LaneRecord, error)
	reg    provider.ProviderRegistryReader
}

// family resolves the provider family of job jobID.
func (r reviewerInputs) family(ctx context.Context, jobID string) (reviewerFamily, error) {
	execID, ok, err := r.latestExecution(ctx, jobID)
	if err != nil || !ok {
		return reviewerFamily{}, err
	}
	rsv, ok, err := r.fleet.ReservationByExecution(ctx, execID)
	if err != nil || !ok {
		return reviewerFamily{}, err
	}
	lanes, err := r.lanes(ctx)
	if err != nil {
		return reviewerFamily{}, err
	}
	for _, l := range lanes {
		if l.LaneName != rsv.LaneID {
			continue
		}
		info, err := r.reg.GetProvider(ctx, l.ProviderName)
		if err != nil {
			return reviewerFamily{}, err
		}
		if info.Driver == "" {
			return reviewerFamily{}, nil
		}
		return reviewerFamily{Family: info.Driver, Resolved: true}, nil
	}
	return reviewerFamily{}, nil
}

// latestExecution returns the id of jobID's highest-attempt execution.
func (r reviewerInputs) latestExecution(ctx context.Context, jobID string) (string, bool, error) {
	var id string
	err := r.jobsDB.QueryRowContext(ctx,
		`SELECT id FROM jobs_execution WHERE job_id = ? ORDER BY attempt DESC, id DESC LIMIT 1`, jobID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "reviewer: latest execution")
	}
	return id, true, nil
}

// AuthorFamilies returns the family of each code-class dependency of job;
// a job with none is KindNotFound.
func (r reviewerInputs) AuthorFamilies(ctx context.Context, job jobs.Job) ([]reviewerFamily, error) {
	deps, err := r.jobs.Dependencies(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	var out []reviewerFamily
	for _, id := range deps {
		dep, ok, err := r.jobs.GetJob(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok || dep.MinTaskClass != "code" {
			continue
		}
		f, err := r.family(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, cascade.Newf(cascade.KindNotFound, "reviewer: job %q has no code-class author", job.ID)
	}
	return out, nil
}

// ReviewerFamily returns the family the review job itself ran on.
func (r reviewerInputs) ReviewerFamily(ctx context.Context, job jobs.Job) (reviewerFamily, error) {
	return r.family(ctx, job.ID)
}
