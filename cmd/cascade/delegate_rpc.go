//go:build !windows

// Purpose: the conductor.delegate and conductor.delegate_wait handlers. delegate
// classifies the params, resolves the parent record (the caller's own delegation,
// or a fresh owner root for the owner CLI), and hands the request to the wired
// delegation service; delegate_wait reads the addressed result through the
// service's origin-checked Wait.
// Inputs: strict JSON params; the caller bound by the access middleware.
// Outputs: {job_id, status "dispatched"} and {job_id, state, output?, artifacts?, data_class}.
// Constraints: every refusal is a typed cascade error; a mutation needs the job's
// worktree binding, which this daemon does not resolve yet, and is refused.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/delegation"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	delegateMethod     = "conductor.delegate"
	delegateWaitMethod = "conductor.delegate_wait"
	delegateIntentMax  = 64 << 10
	delegateWaitMax    = 10 * time.Minute
)

var (
	_ = rpc.DeclareAccess(delegateMethod, rpc.AccessOwnerView)
	_ = rpc.DeclareAccess(delegateWaitMethod, rpc.AccessOwnerView)
)

// errDelegateMutationUnbound refuses a mutation: the job worktree binding is not wired.
var errDelegateMutationUnbound = cascade.New(cascade.KindUnavailable,
	"conductor.delegate: mutation delegations need the job's worktree binding, which this daemon does not resolve yet")

// delegateParams are the conductor.delegate params.
type delegateParams struct {
	Intent        string `json:"intent"`
	TaskClass     string `json:"task_class,omitempty"`
	MinQuality    string `json:"min_quality,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
	Mutation      bool   `json:"mutation"`
	JobID         string `json:"job_id,omitempty"`
	WaitTimeoutMS int64  `json:"wait_timeout_ms,omitempty"`
}

// delegateWaitParams are the conductor.delegate_wait params.
type delegateWaitParams struct {
	JobID     string `json:"job_id"`
	TimeoutMS int64  `json:"timeout_ms"`
}

// delegateResult is the conductor.delegate reply.
type delegateResult struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// delegateHandlers are the two verbs over the wired runtime.
type delegateHandlers struct{ rt *delegateRuntime }

// mountDelegate registers both verbs on reg.
func mountDelegate(reg *rpc.Registry, rt *delegateRuntime) {
	h := delegateHandlers{rt: rt}
	reg.Register(delegateMethod, h.delegate)
	reg.Register(delegateWaitMethod, h.wait)
}

// delegate classifies the params, resolves the parent and delegates.
func (h delegateHandlers) delegate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p delegateParams
	if err := decodeDelegateParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Intent == "" || len(p.Intent) > delegateIntentMax || p.WaitTimeoutMS < 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "conductor.delegate: intent is required (at most 64 KiB) and wait_timeout_ms is not negative")
	}
	need, policy, err := conductor.ClassifyDelegation(conductor.DelegationNeed{TaskClass: p.TaskClass,
		MinQuality: p.MinQuality, Deadline: p.Deadline, Mutation: p.Mutation, JobID: p.JobID})
	if err != nil {
		return nil, err
	}
	if need.Mutation {
		return nil, errDelegateMutationUnbound
	}
	svc, err := h.rt.service(ctx)
	if err != nil {
		return nil, err
	}
	agentID, err := newDelegateID("delegate-")
	if err != nil {
		return nil, err
	}
	parent, wt, finish, err := h.rt.parent(ctx, policy)
	if err != nil {
		return nil, err
	}
	resp, err := svc.Delegate(ctx, &delegation.Request{AgentID: agentID, Need: need.TaskClass,
		Deadline: need.Deadline, MinQuality: need.MinQuality, JobID: need.JobID, Parent: parent, Worktree: wt})
	if err != nil {
		_ = wt.Release()
	}
	if err = cascadeOrInternal(finish(ctx, err), "conductor.delegate"); err != nil {
		return nil, err
	}
	return delegateResult{JobID: resp.Record.Env.ID, Status: "dispatched"}, nil
}

// wait returns the origin's addressed result, or the stored state at the timeout.
func (h delegateHandlers) wait(ctx context.Context, raw json.RawMessage) (any, error) {
	var p delegateWaitParams
	if err := decodeDelegateParams(raw, &p); err != nil {
		return nil, err
	}
	if p.JobID == "" || p.TimeoutMS < 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "conductor.delegate_wait: job_id is required and timeout_ms is not negative")
	}
	svc, err := h.rt.service(ctx)
	if err != nil {
		return nil, err
	}
	timeout := min(time.Duration(p.TimeoutMS)*time.Millisecond, delegateWaitMax)
	res, err := svc.Wait(ctx, p.JobID, timeout)
	return res, cascadeOrInternal(err, "conductor.delegate_wait")
}

// callerParent is the stored delegation of a principal caller; one without a
// delegation record of its own is refused.
func callerParent(ctx context.Context, store *jobs.Store, c principal.Caller) (jobs.DelegationRecord, error) {
	rec, ok, err := store.DelegationByAgentPrincipal(ctx, c.Principal.ID)
	if err != nil {
		return jobs.DelegationRecord{}, err
	}
	if !ok {
		return jobs.DelegationRecord{}, cascade.New(cascade.KindPermissionDenied, "conductor.delegate: the caller holds no delegation to delegate from")
	}
	return rec, nil
}

// decodeDelegateParams decodes raw into out, refusing unknown fields and trailing data.
func decodeDelegateParams(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "conductor.delegate: params")
	}
	if _, err := dec.Token(); err != io.EOF {
		return cascade.New(cascade.KindInvalidInput, "conductor.delegate: params carry trailing data")
	}
	return nil
}

// cascadeOrInternal returns err unchanged when it carries a Kind, else wrapped KindInternal.
func cascadeOrInternal(err error, verb string) error {
	if err == nil {
		return nil
	}
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrap(cascade.KindInternal, err, verb)
}

// service returns the built service, building it on first use; a failed build is retried.
func (rt *delegateRuntime) service(ctx context.Context) (*delegation.Service, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.svc != nil {
		return rt.svc, nil
	}
	if rt.build == nil {
		return nil, cascade.Newf(cascade.KindUnavailable, "conductor.delegate: the delegation service is not wired: %s", rt.reason)
	}
	svc, signer, err := rt.build(ctx)
	if err != nil {
		return nil, cascadeOrInternal(err, "conductor.delegate: build the delegation service")
	}
	rt.svc, rt.signer = svc, signer
	return svc, nil
}

// newDelegateID returns prefix and 16 random bytes in hex.
func newDelegateID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "conductor.delegate: random id")
	}
	return prefix + hex.EncodeToString(b), nil
}

// registeringShim records every spawn in live from before the child runs
// until its handle's release (which sweeps the lease) returns nil.
type registeringShim struct {
	spawn interface {
		Spawn(ctx context.Context, req agents.SpawnRequest) (agents.SpawnHandle, error)
	}
	live *agents.SpawnRegistry
}

// Spawn registers the marker, spawns, then records the process group.
func (r registeringShim) Spawn(ctx context.Context, req agents.SpawnRequest) (agents.SpawnHandle, error) {
	entry := r.live.Register(agents.LiveSpawn{Marker: req.LeaseMarker})
	h, err := r.spawn.Spawn(ctx, req)
	if err != nil {
		entry.Release()
		return h, err
	}
	entry.Set(agents.LiveSpawn{Marker: req.LeaseMarker, PGID: h.PGID})
	inner := h.Release
	h.Release = func(ctx context.Context) error {
		var err error
		if inner != nil {
			err = inner(ctx)
		}
		if err == nil {
			entry.Release()
		}
		return err
	}
	return h, nil
}
