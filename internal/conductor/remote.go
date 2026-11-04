package conductor

// Purpose: the remote-dispatch seam for work placed on an enrolled node.
//   A selection that names a NodeID runs ONLY through a bound
//   RemoteDispatcher; a selection without one runs on the local provider
//   exactly as it did before this seam existed.
// Inputs: a RouteCall (the model request plus the action/entity identity
//   and resume reference the remote side needs), an optional explicit
//   NodeSelection, the router, the local runner and the remote dispatcher.
// Outputs: the provider.ModelResponse of whichever side ran the call, or a
//   taxonomy error.
// Constraints: there is no path from a NodeID selection to the local
//   provider or to another node. An unbound dispatcher is KindUnavailable
//   (ErrRemoteUnavailable), never a local fallback. Placement picks the
//   first eligible node in the engine's deterministic order.
// SPORT: conductor.remote dispatch seam (ADD) — P1-NODE-02.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// NodeSelection is a lane Selection plus the enrolled node placement chose
// for it. An empty NodeID means the call runs on the local provider.
type NodeSelection struct {
	provider.Selection
	// NodeID names the enrolled node the work is placed on, or is empty
	// for local execution.
	NodeID string
}

// RemoteDispatch is everything the remote side receives for one call.
type RemoteDispatch struct {
	// NodeID is the node the work must run on; it is never rewritten.
	NodeID string
	// ActionID identifies the action being dispatched.
	ActionID string
	// EntityID identifies the entity the action acts on.
	EntityID string
	// Resume is the caller's resume reference, passed through unchanged.
	Resume string
	// Selection is the lane selection the call was placed with.
	Selection provider.Selection
	// Request is the model request, passed through unchanged.
	Request provider.ModelRequest
}

// RemoteDispatcher runs a node-placed call on that node.
type RemoteDispatcher interface {
	DispatchRemote(ctx context.Context, d RemoteDispatch) (provider.ModelResponse, error)
}

// LocalRunner runs a call on the local provider under sel.
type LocalRunner func(ctx context.Context, sel provider.Selection, req provider.ModelRequest) (provider.ModelResponse, error)

// RouteCall is one call through a RemoteRoute.
type RouteCall struct {
	// Request is the model request to run.
	Request provider.ModelRequest
	// ActionID identifies the action, forwarded to a remote dispatch.
	ActionID string
	// EntityID identifies the entity, forwarded to a remote dispatch.
	EntityID string
	// Resume is the resume reference, forwarded to a remote dispatch.
	Resume string
	// Selection, when non-nil, is used as given instead of asking the
	// router; its NodeID still decides local versus remote.
	Selection *NodeSelection
}

// ErrRemoteUnavailable is the refusal a NodeID selection gets when no
// RemoteDispatcher is bound. It is never answered by running locally.
var ErrRemoteUnavailable = cascade.New(cascade.KindUnavailable,
	"conductor: a node was selected but no remote dispatcher is bound")

// RemoteRoute joins the router, the local provider and the remote
// dispatcher. Build one with NewRemoteRoute; the zero value is not usable.
type RemoteRoute struct {
	router *DefaultRouter
	local  LocalRunner
	remote RemoteDispatcher
}

// NewRemoteRoute builds a RemoteRoute with no remote dispatcher bound.
func NewRemoteRoute(router *DefaultRouter, local LocalRunner) *RemoteRoute {
	return &RemoteRoute{router: router, local: local}
}

// WithRemoteDispatcher binds d and returns rr. A nil d leaves the route
// unbound, so every NodeID selection refuses with ErrRemoteUnavailable.
func (rr *RemoteRoute) WithRemoteDispatcher(d RemoteDispatcher) *RemoteRoute {
	rr.remote = d
	return rr
}

// Place runs the router's selection and sets NodeID from the placement
// consult: the first eligible node when the request names node
// capabilities, empty otherwise.
func (rr *RemoteRoute) Place(ctx context.Context, req provider.ModelRequest) (NodeSelection, error) {
	eligible, _, err := rr.router.eligibleNodes(req, nil)
	if err != nil {
		return NodeSelection{}, err
	}
	sel, err := rr.router.Select(ctx, req)
	if err != nil {
		return NodeSelection{}, err
	}
	out := NodeSelection{Selection: sel}
	if len(eligible) > 0 {
		out.NodeID = eligible[0].NodeID
	}
	return out, nil
}

// Run places call (unless it carries an explicit Selection) and runs it:
// remotely through the bound dispatcher when a NodeID is set, locally
// otherwise.
func (rr *RemoteRoute) Run(ctx context.Context, call RouteCall) (provider.ModelResponse, error) {
	var sel NodeSelection
	if call.Selection != nil {
		sel = *call.Selection
	} else {
		placed, err := rr.Place(ctx, call.Request)
		if err != nil {
			return provider.ModelResponse{}, err
		}
		sel = placed
	}
	if sel.NodeID == "" {
		return rr.local(ctx, sel.Selection, call.Request)
	}
	if rr.remote == nil {
		return provider.ModelResponse{}, cascade.Wrapf(cascade.KindUnavailable, ErrRemoteUnavailable,
			"conductor: node %q action %q entity %q", sel.NodeID, call.ActionID, call.EntityID)
	}
	return rr.remote.DispatchRemote(ctx, RemoteDispatch{
		NodeID:    sel.NodeID,
		ActionID:  call.ActionID,
		EntityID:  call.EntityID,
		Resume:    call.Resume,
		Selection: sel.Selection,
		Request:   call.Request,
	})
}
