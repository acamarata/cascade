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
	"errors"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
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
	// Driver is the registered agent driver the leased lane resolved to.
	Driver string
	// Record is the child delegation record signed with the installation key.
	Record envelope.Signed
}

// ErrInvalidParam refuses a mutation request that names no job_id.
var ErrInvalidParam = cascade.New(cascade.KindInvalidInput, "delegation: job_id is required for a mutation")

// fleetLeaser is the Fleet seam Delegate leases capacity through;
// *capacity.Fleet satisfies it.
type fleetLeaser interface {
	Lease(ctx context.Context, req capacity.ResourceRequest, d capacity.DispatchRef, attempt int) (capacity.Lease, error)
	Release(ctx context.Context, l capacity.Lease) error
}

// Deps contains the injected dependencies for the delegation service.
type Deps struct {
	// Clock is the injected runtime.Clock.
	Clock runtime.Clock
	// Store is the jobs store holding the delegation records.
	Store *jobs.Store
	// Pinned is the controller public key every envelope verifies under.
	Pinned ed25519.PublicKey
	// Signer is the installation key custody; on a node-role daemon it
	// refuses KindPermissionDenied.
	Signer envelope.Signer
	// fleet leases capacity once the parent record verifies.
	fleet fleetLeaser
}

// Service is the delegation service.
type Service struct {
	deps  Deps
	lanes map[string]string
}

// NewService returns a configured service instance.
func NewService(deps Deps) *Service {
	return &Service{deps: deps, lanes: map[string]string{}}
}

// SetAgentLanes registers the agent drivers whose "agent:<id>" lanes a
// lease may resolve to; it replaces any earlier registration.
func (s *Service) SetAgentLanes(lanes []capacity.AgentLane) {
	m := make(map[string]string, len(lanes))
	for _, l := range lanes {
		if l.DriverID != "" {
			m["agent:"+l.DriverID] = l.DriverID
		}
	}
	s.lanes = m
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
	penv, err := s.verifyParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	rec, err := s.signChild(ctx, penv, req.AgentID)
	if err != nil {
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
	driver, ok := s.lanes[string(lease.LaneID)]
	if !ok {
		if rerr := s.deps.fleet.Release(ctx, lease); rerr != nil {
			return nil, rerr
		}
		return nil, cascade.New(cascade.KindUnavailable, "delegation: leased lane "+string(lease.LaneID)+" resolves to no registered agent driver")
	}
	resp := &Response{Accepted: true, LeaseID: lease.ID, ToolPolicy: "readonly", InScopePrefixes: []string{}, Driver: driver, Record: rec}
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
func (s *Service) verifyParent(ctx context.Context, parent jobs.DelegationRecord) (envelope.Envelope, error) {
	if s.deps.Clock == nil || len(s.deps.Pinned) != ed25519.PublicKeySize {
		return envelope.Envelope{}, invalidf("Delegate: a clock and a pinned controller key are required")
	}
	if parent.ID == "" || len(parent.Signed) == 0 {
		return envelope.Envelope{}, integrityf("parent: the request carries no signed parent record")
	}
	return VerifyChain(ctx, s.deps.Store, parent, s.deps.Pinned, s.deps.Clock.Now())
}

// signChild signs the delegated child record for agentID under the
// installation key: the verified parent's scope, re-parented to it. A
// node-role signer's KindPermissionDenied comes back unchanged.
func (s *Service) signChild(ctx context.Context, penv envelope.Envelope, agentID string) (envelope.Signed, error) {
	if s.deps.Signer == nil {
		return envelope.Signed{}, invalidf("Delegate: an installation key signer is required")
	}
	pd, err := envelope.Digest(penv)
	if err != nil {
		return envelope.Signed{}, ruleErr("parent", err)
	}
	id, err := newID()
	if err != nil {
		return envelope.Signed{}, err
	}
	child := penv
	child.ID, child.Principal, child.ParentDigest = id, agentID, pd
	signed, err := envelope.Issue(ctx, s.deps.Signer, child)
	if inner := errors.Unwrap(err); inner != nil && cascade.HasKind(inner, cascade.KindPermissionDenied) {
		return envelope.Signed{}, cascade.Wrap(cascade.KindPermissionDenied, inner, "delegation: sign")
	}
	return signed, err
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
