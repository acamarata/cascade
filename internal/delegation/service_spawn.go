// Purpose (this file): the spawn half of Service.Delegate: the child record is stored with
// its lease's ids, projected (spawn_envelope.go), minted a principal, marked, gated, spawned
// and bound.

package delegation

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// spawn stores the child record, projects its envelope, mints its principal, binds the marker,
// gates, spawns and commits; an empty lease id or child principal refuses ErrSpawnBinding
// before any Spawn, and a record that does not verify under the pinned key spawns nothing.
func (s *Service) spawn(ctx context.Context, req *Request, rec envelope.Signed, lease capacity.Lease, policy string) error {
	if s.shim == nil || s.deps.Minter == nil {
		return s.abort(ctx, lease, "", invalidf("Delegate: a shim and a minter are required"))
	}
	id, child := rec.Env.ID, rec.Env.Principal
	if child == "" { // the store refuses a principal-less record: refuse before the insert
		return s.abort(ctx, lease, "", ErrSpawnBinding)
	}
	if err := s.insertChild(ctx, req, rec, lease); err != nil {
		return s.abort(ctx, lease, "", err)
	}
	job, err := s.spawnJob(req, rec, policy)
	tok, marker := "", ""
	if err == nil {
		tok, err = s.mintSession(ctx, req, rec.Env, job.JobID)
	}
	if err == nil {
		marker, err = agents.NewLeaseMarker()
	}
	if err == nil {
		err = s.deps.Store.BindDelegationMarker(ctx, id, marker)
	}
	if err == nil {
		err = s.runGates(ctx, id, rec.Env)
	}
	if err == nil && lease.ID == "" {
		err = ErrSpawnBinding
	}
	var h agents.SpawnHandle
	if err == nil {
		h, err = s.shim.Spawn(ctx, agents.SpawnRequest{Lane: string(lease.LaneID), Job: job, PrincipalToken: tok,
			LeaseMarker: marker, LeaseID: lease.ID, ChildPrincipal: child})
	}
	if err == nil {
		if err = s.deps.Store.BindDelegationSpawn(ctx, id, lease.ID, string(h.JobID)); err != nil {
			_ = h.Release(ctx)
		}
	}
	if err != nil { // the minted token never outlives a refused spawn
		s.deps.Minter.Revoke(child)
		return s.abort(ctx, lease, id, err)
	}
	return s.started(ctx, req, rec, lease, h)
}

// insertChild stores the signed child record live on the lease's lane, with the lease's
// execution and reservation ids so a restart can rebuild the lease.
func (s *Service) insertChild(ctx context.Context, req *Request, rec envelope.Signed, lease capacity.Lease) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "delegation: encode the child record")
	}
	return s.deps.Store.InsertDelegation(ctx, jobs.DelegationRecord{ID: rec.Env.ID, Signed: b,
		AgentPrincipal: rec.Env.Principal, JobID: req.JobID, Mutation: req.Mutation,
		Lane: string(lease.LaneID), ExecutionID: lease.ExecutionID, ReservationID: lease.ReservationID,
		State: jobs.DelegationLive, CreatedAt: s.deps.Clock.Now()})
}
