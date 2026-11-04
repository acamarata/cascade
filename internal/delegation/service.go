// Purpose (this file): delegation.Service core: Deps, NewService and
// the Delegate RPC method. Delegate applies request defaults, then
// verifies the parent delegation record's whole signed chain
// (VerifyChain at the injected clock's now) before it asks the Fleet for
// a lease. An unsigned, tampered, expired or wider parent refuses with
// no Lease call.
//
// Inputs: a configured Deps with injected runtime.Clock, the jobs store,
// the pinned controller key and the unexported fleet seam.
// Outputs: a configured Service instance.
//
// SPORT: delegation.service (ADD, P1-AGT-08).

package delegation

import (
	"context"
	"crypto/ed25519"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Request is a delegation request.
type Request struct {
	// AgentID is the target agent.
	AgentID string
	// Lane is the agent lane.
	Lane string
	// Need is the classified delegation need.
	Need string
	// JobID is the job id (required for mutations).
	JobID string
	// Mutation is true when the delegated work may write the job's repo.
	Mutation bool
	// JobProject is the project of the job named by JobID.
	JobProject string
	// InScope is the job row's in-scope path prefixes.
	InScope []string
	// Deadline is the requested deadline.
	Deadline string
	// MinQuality is the requested minimum quality.
	MinQuality string
	// Parent is the caller's copy of the parent delegation record; it
	// must verify against the stored chain before any lease.
	Parent jobs.DelegationRecord
}

// Response is a delegation response.
type Response struct {
	// Accepted is true if the delegation was accepted.
	Accepted bool
	// LeaseID is the Fleet lease granted to the delegation.
	LeaseID string
	// InScopePrefixes are the job's in-scope prefixes; empty without a job.
	InScopePrefixes []string
	// ToolPolicy is "all" for a mutation and "readonly" otherwise.
	ToolPolicy string
}

// ErrInvalidParam refuses a mutation request that names no job_id.
var ErrInvalidParam = cascade.New(cascade.KindInvalidInput, "delegation: job_id is required for a mutation")

// fleetLeaser is the Fleet seam Delegate leases capacity through;
// *capacity.Fleet satisfies it.
type fleetLeaser interface {
	Lease(ctx context.Context, req capacity.ResourceRequest, d capacity.DispatchRef, attempt int) (capacity.Lease, error)
}

// Deps contains the injected dependencies for the delegation service.
type Deps struct {
	// Clock is the injected runtime.Clock.
	Clock runtime.Clock
	// Store is the jobs store holding the delegation records.
	Store *jobs.Store
	// Pinned is the controller public key every envelope verifies under.
	Pinned ed25519.PublicKey
	// fleet leases capacity once the parent record verifies.
	fleet fleetLeaser
}

// Service is the delegation service.
type Service struct {
	deps Deps
}

// NewService returns a configured service instance.
func NewService(deps Deps) *Service {
	return &Service{deps: deps}
}

// Delegate applies request defaults, verifies the parent record and only
// then leases capacity from the Fleet.
func (s *Service) Delegate(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "delegation: nil request")
	}
	s.applyDefaults(req)
	if req.Mutation && req.JobID == "" {
		return nil, ErrInvalidParam
	}
	if err := checkJobScope(ctx, req); err != nil {
		return nil, err
	}
	if err := s.verifyParent(ctx, req.Parent); err != nil {
		return nil, err
	}
	if s.deps.fleet == nil {
		return nil, invalidf("Delegate: a fleet is required")
	}
	lease, err := s.deps.fleet.Lease(ctx, capacity.ResourceRequest{Intent: req.Need},
		capacity.DispatchRef{JobID: req.JobID, AgentOnly: true}, 0)
	if err != nil {
		return nil, err
	}
	resp := &Response{Accepted: true, LeaseID: lease.ID, ToolPolicy: "readonly", InScopePrefixes: []string{}}
	if req.JobID != "" {
		resp.InScopePrefixes = append(resp.InScopePrefixes, req.InScope...)
	}
	if req.Mutation {
		resp.ToolPolicy = "all"
	}
	return resp, nil
}

// checkJobScope refuses a principal caller whose Scope differs from the
// job's project. The owner CLI carries no principal and may delegate on
// any job.
func checkJobScope(ctx context.Context, req *Request) error {
	c, ok := principal.CallerFrom(ctx)
	if !ok || c.Principal == nil || req.JobID == "" {
		return nil
	}
	if c.Principal.Scope != req.JobProject {
		return deniedf("job %s is outside the caller scope %q", req.JobID, c.Principal.Scope)
	}
	return nil
}

// verifyParent refuses a parent record whose stored chain does not
// verify under the pinned key at the injected clock's now.
func (s *Service) verifyParent(ctx context.Context, parent jobs.DelegationRecord) error {
	if s.deps.Clock == nil || len(s.deps.Pinned) != ed25519.PublicKeySize {
		return invalidf("Delegate: a clock and a pinned controller key are required")
	}
	if parent.ID == "" || len(parent.Signed) == 0 {
		return integrityf("parent: the request carries no signed parent record")
	}
	_, err := VerifyChain(ctx, s.deps.Store, parent, s.deps.Pinned, s.deps.Clock.Now())
	return err
}

// applyDefaults populates missing fields with sensible defaults.
func (s *Service) applyDefaults(req *Request) {
	if req.Deadline == "" {
		req.Deadline = "standard"
	}
	if req.Need == "" {
		req.Need = "compute"
	}
	if req.MinQuality == "" {
		req.MinQuality = "standard"
	}
}
