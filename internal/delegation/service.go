// Purpose (this file): delegation.Service: Delegate verifies the parent
// chain, resolves a mutation's DispatchRef.RepoID (RTM-CI-17), leases,
// then spawns: entitlement, child record, session principal, marker,
// PreSpawnGates, shim Spawn, BindDelegationSpawn and Commit.
// SPORT: delegation.service (ADD, P1-AGT-08).

package delegation

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// Request is a delegation request (a mutation needs JobID); JobProject, InScope and RepoRoot (the
// worktree repo root) come from the job; Parent is the caller's copy, verified against the chain.
type Request struct {
	AgentID, Lane, Need, JobID, JobProject, RepoRoot, Deadline, MinQuality string
	Mutation                                                               bool
	InScope                                                                []string
	Parent                                                                 jobs.DelegationRecord
}

// Response carries the lease, in-scope prefixes, tool policy, driver and signed child record.
type Response struct {
	Accepted                    bool
	LeaseID, ToolPolicy, Driver string
	InScopePrefixes             []string
	Record                      envelope.Signed
}

// ErrInvalidParam refuses a mutation request that names no job_id.
var ErrInvalidParam = cascade.New(cascade.KindInvalidInput, "delegation: job_id is required for a mutation")

// fleetLeaser is the Fleet seam; *capacity.Fleet satisfies it.
type fleetLeaser interface {
	Lease(ctx context.Context, req capacity.ResourceRequest, d capacity.DispatchRef, attempt int) (capacity.Lease, error)
	Commit(ctx context.Context, l capacity.Lease) error
	Abort(ctx context.Context, l capacity.Lease) error
	Release(ctx context.Context, l capacity.Lease) error
}

// spawner is the shim seam; *agents.Shim satisfies it.
type spawner interface {
	Spawn(ctx context.Context, req agents.SpawnRequest) (agents.SpawnHandle, error)
}

// Deps are the injected dependencies: Signer is the installation key (a node role refuses),
// Shim the only Spawn caller, RepoIDOf a repo root's registered id (nil in production: none).
type Deps struct {
	Clock    runtime.Clock
	Store    *jobs.Store
	Pinned   ed25519.PublicKey
	Signer   envelope.Signer
	Minter   *principal.Minter
	Shim     spawner
	RepoIDOf func(ctx context.Context, root string) (string, error)
	fleet    fleetLeaser
}

// Service is the delegation service.
type Service struct {
	deps  Deps
	lanes map[string]capacity.AgentLane
	shim  spawner
}

// NewService returns a configured service instance.
func NewService(deps Deps) *Service {
	return &Service{deps: deps, lanes: map[string]capacity.AgentLane{}, shim: deps.Shim}
}

// SetAgentLanes registers the agent drivers whose "agent:<id>" lanes a
// lease may resolve to; it replaces any earlier registration.
func (s *Service) SetAgentLanes(lanes []capacity.AgentLane) {
	m := make(map[string]capacity.AgentLane, len(lanes))
	for _, l := range lanes {
		if l.DriverID != "" {
			m["agent:"+l.DriverID] = l
		}
	}
	s.lanes = m
}

// Delegate verifies the parent record, leases capacity and spawns the agent via the shim.
func (s *Service) Delegate(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "delegation: nil request")
	}
	req.Deadline, req.Need, req.MinQuality = cmp.Or(req.Deadline, "standard"), cmp.Or(req.Need, "compute"), cmp.Or(req.MinQuality, "standard")
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
	repoID, err := s.repoID(ctx, req)
	if err != nil { // refuse the stored child record before any lease
		return nil, cmp.Or(s.insertChild(ctx, req, rec, ""),
			s.deps.Store.FinishDelegation(ctx, rec.Env.ID, jobs.DelegationRefused, err.Error()), err)
	}
	lease, driver, err := s.lease(ctx, req, repoID)
	if err != nil {
		return nil, err
	}
	resp := &Response{Accepted: true, LeaseID: lease.ID, ToolPolicy: "readonly", InScopePrefixes: []string{}, Driver: driver, Record: rec}
	if req.JobID != "" {
		resp.InScopePrefixes = append(resp.InScopePrefixes, req.InScope...)
	}
	if req.Mutation {
		resp.ToolPolicy = "all"
	}
	if err := s.spawn(ctx, req, rec, lease, resp.ToolPolicy); err != nil {
		return nil, err
	}
	return resp, nil
}

// repoID resolves a mutation's RepoRoot through RepoIDOf: a nil seam, a reason-only
// request or KindNotFound is no repo (""); any other error is returned as is.
func (s *Service) repoID(ctx context.Context, req *Request) (string, error) {
	if !req.Mutation || s.deps.RepoIDOf == nil {
		return "", nil
	}
	id, err := s.deps.RepoIDOf(ctx, req.RepoRoot)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return "", nil
	}
	return id, err
}

// lease leases (DispatchRef.RepoID = repoID) and resolves the lane's driver (Release
// when none); no programmatic entitlement Aborts before ErrEntitlement.
func (s *Service) lease(ctx context.Context, req *Request, repoID string) (capacity.Lease, string, error) {
	if s.deps.fleet == nil {
		return capacity.Lease{}, "", invalidf("Delegate: a fleet is required")
	}
	lease, err := s.deps.fleet.Lease(ctx, capacity.ResourceRequest{Intent: req.Need},
		capacity.DispatchRef{JobID: req.JobID, RepoID: repoID, AgentOnly: true}, 0)
	if err != nil {
		return capacity.Lease{}, "", err
	}
	lane, ok := s.lanes[string(lease.LaneID)]
	if !ok {
		return capacity.Lease{}, "", cmp.Or[error](s.deps.fleet.Release(ctx, lease), cascade.New(cascade.KindUnavailable, "delegation: leased lane "+string(lease.LaneID)+" resolves to no registered agent driver"))
	}
	if !lane.Posture.ProgrammaticEntitlement {
		return capacity.Lease{}, "", s.abort(ctx, lease, "", provider.ErrEntitlement)
	}
	return lease, lane.DriverID, nil
}

// spawn stores the child record, mints its principal, binds the marker, gates, spawns and commits.
func (s *Service) spawn(ctx context.Context, req *Request, rec envelope.Signed, lease capacity.Lease, policy string) error {
	if s.shim == nil || s.deps.Minter == nil {
		return s.abort(ctx, lease, "", invalidf("Delegate: a shim and a minter are required"))
	}
	id := rec.Env.ID
	if err := s.insertChild(ctx, req, rec, string(lease.LaneID)); err != nil {
		return s.abort(ctx, lease, "", err)
	}
	job := provider.AgentJobSpec{JobID: provider.AgentJobID(id), ToolPolicy: policy}
	tok, err := s.mintSession(ctx, req, rec.Env, job.JobID)
	if err != nil {
		return s.abort(ctx, lease, id, err)
	}
	marker, err := agents.NewLeaseMarker()
	if err == nil {
		err = s.deps.Store.BindDelegationMarker(ctx, id, marker)
	}
	if err == nil {
		err = s.runGates(ctx, id, rec.Env)
	}
	if err != nil {
		return s.abort(ctx, lease, id, err)
	}
	h, err := s.shim.Spawn(ctx, agents.SpawnRequest{Lane: string(lease.LaneID), Job: job, PrincipalToken: tok,
		LeaseMarker: marker, LeaseID: lease.ID, ChildPrincipal: rec.Env.Principal})
	if err != nil {
		return s.abort(ctx, lease, id, err)
	}
	if err := s.deps.Store.BindDelegationSpawn(ctx, id, lease.ID, string(h.JobID)); err != nil {
		_ = h.Release(ctx)
		return s.abort(ctx, lease, id, err)
	}
	return s.deps.fleet.Commit(ctx, lease)
}

// insertChild stores the signed child record live on lane.
func (s *Service) insertChild(ctx context.Context, req *Request, rec envelope.Signed, lane string) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "delegation: encode the child record")
	}
	return s.deps.Store.InsertDelegation(ctx, jobs.DelegationRecord{ID: rec.Env.ID, Signed: b,
		AgentPrincipal: rec.Env.Principal, JobID: req.JobID, Mutation: req.Mutation,
		Lane: lane, State: jobs.DelegationLive, CreatedAt: s.deps.Clock.Now()})
}

// mintSession mints the child's node-job principal, SessionID = provider.SessionIDFor(job).
func (s *Service) mintSession(ctx context.Context, req *Request, env envelope.Envelope, job provider.AgentJobID) (string, error) {
	digest, err := envelope.Digest(env)
	if err != nil {
		return "", ruleErr("child", err)
	}
	p := principal.Principal{ID: env.Principal, Kind: principal.KindNodeJob,
		SessionID: provider.SessionIDFor(job), Scope: req.JobProject, EnvelopeDigest: digest}
	if c, _ := principal.CallerFrom(ctx); c.Principal != nil {
		p.ParentSessionID, p.Scope = c.Principal.SessionID, cmp.Or(p.Scope, c.Principal.Scope)
	}
	if p.Scope = cmp.Or(p.Scope, env.ID); !strings.HasPrefix(p.Scope, "project:") {
		p.Scope = "project:" + p.Scope
	}
	return s.deps.Minter.Mint(p, min(env.ExpiresAt.Sub(s.deps.Clock.Now()), 24*time.Hour))
}

// runGates runs every registered PreSpawnGate over the stored record.
func (s *Service) runGates(ctx context.Context, id string, env envelope.Envelope) error {
	rec, err := s.deps.Store.GetDelegation(ctx, id)
	for _, g := range PreSpawnGates() {
		if err != nil {
			return err
		}
		err = g(ctx, rec, env)
	}
	return err
}

// abort Aborts lease, refuses record id (if any) with cause, returns cause.
func (s *Service) abort(ctx context.Context, lease capacity.Lease, id string, cause error) error {
	err := s.deps.fleet.Abort(ctx, lease)
	if err == nil && id != "" {
		err = s.deps.Store.FinishDelegation(ctx, id, jobs.DelegationRefused, cause.Error())
	}
	return cmp.Or(err, cause)
}

// checkJobScope refuses a principal caller outside the job's project (the owner CLI has none).
func checkJobScope(ctx context.Context, req *Request) error {
	if c, ok := principal.CallerFrom(ctx); ok && c.Principal != nil && req.JobID != "" && c.Principal.Scope != req.JobProject {
		return deniedf("job %s is outside the caller scope %q", req.JobID, c.Principal.Scope)
	}
	return nil
}

// verifyParent refuses a parent whose stored chain does not verify now.
func (s *Service) verifyParent(ctx context.Context, parent jobs.DelegationRecord) (envelope.Envelope, error) {
	if s.deps.Clock == nil || len(s.deps.Pinned) != ed25519.PublicKeySize {
		return envelope.Envelope{}, invalidf("Delegate: a clock and a pinned controller key are required")
	}
	if parent.ID == "" || len(parent.Signed) == 0 {
		return envelope.Envelope{}, integrityf("parent: the request carries no signed parent record")
	}
	return VerifyChain(ctx, s.deps.Store, parent, s.deps.Pinned, s.deps.Clock.Now())
}

// signChild signs agentID's child record under the installation key, re-parented
// to the verified parent; a node-role signer's KindPermissionDenied comes back.
func (s *Service) signChild(ctx context.Context, penv envelope.Envelope, agentID string) (envelope.Signed, error) {
	if s.deps.Signer == nil {
		return envelope.Signed{}, invalidf("Delegate: an installation key signer is required")
	}
	pd, err := envelope.Digest(penv)
	if err != nil {
		return envelope.Signed{}, ruleErr("parent", err)
	}
	child := penv
	if child.ID, err = newID(); err != nil {
		return envelope.Signed{}, err
	}
	child.Principal, child.ParentDigest = agentID, pd
	signed, err := envelope.Issue(ctx, s.deps.Signer, child)
	if inner := errors.Unwrap(err); inner != nil && cascade.HasKind(inner, cascade.KindPermissionDenied) {
		return envelope.Signed{}, cascade.Wrap(cascade.KindPermissionDenied, inner, "delegation: sign")
	}
	return signed, err
}
