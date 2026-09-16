package nodes

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the controller-side NodeCaller for the D-24 reverse
//
//	tunnel — publish an attempt, then wait for the node to report it.
//
// Inputs: an attempt from the ship leg; a claim and a signed frame from the
//
//	node, arriving over its own tunnel.
//
// Outputs: the node's signed DispatchFrame, handed back to Ship.
// Constraints: DIRECTION. The S-36.T3 tunnel is a REVERSE forward (ssh -R
//
//	streamlocal, the D-24 pattern): the node connects out and its
//	connections arrive at the controller's local RPC socket
//	(reconnect.go's TunnelConfig.LocalDial). The node is therefore the RPC
//	CLIENT and the controller the server, so "the controller calls the
//	node" has no transport under it. NodeCaller is an interface precisely
//	so that direction stays an implementation detail: Ship's sequence is
//	unchanged, and this implementation satisfies its synchronous shape by
//	publishing the attempt and blocking until the node's report arrives.
//	See journals/RULING-dispatch-direction-reverse-tunnel.md.
//
//	Every refusal here is fail-closed and typed. A report nobody is waiting
//	for is REFUSED rather than dropped: silently discarding it would leave
//	the node believing it had handed over a result the controller never
//	recorded, which is the exact ambiguity S-37.T3's recovery must not have
//	to guess about.
//
// SPORT: internal/nodes:dispatch-rendezvous (ADD) — P1-E17-W4-S37-T2.

// Rendezvous matches a controller's published attempt with the node's
// signed report of it.
//
// The zero value is not usable; call NewRendezvous.
type Rendezvous struct {
	mu      sync.Mutex
	pending map[string]*pendingDispatch
}

// pendingDispatch is one published attempt awaiting its node.
type pendingDispatch struct {
	attempt Attempt
	claimed bool
	reply   chan DispatchFrame
}

// NewRendezvous builds a controller's dispatch rendezvous.
func NewRendezvous() *Rendezvous {
	return &Rendezvous{pending: make(map[string]*pendingDispatch)}
}

// Call publishes attempt and blocks until the node reports it.
//
// It implements NodeCaller. A context that ends first is reported as such
// by returning the context's error: the branch is already pushed by the
// time Ship calls this, so work may be running on the node, and Ship maps
// that to a DROPPED TUNNEL rather than to an unreachable node.
func (r *Rendezvous) Call(ctx context.Context, nodeID string, attempt Attempt) (DispatchFrame, error) {
	waiter, err := r.publish(nodeID, attempt)
	if err != nil {
		return DispatchFrame{}, err
	}
	defer r.withdraw(attempt.DispatchID)

	select {
	case frame := <-waiter:
		return frame, nil
	case <-ctx.Done():
		return DispatchFrame{}, ctx.Err()
	}
}

// publish records attempt as the one this controller is waiting on.
//
// A second concurrent publish for the same dispatch is refused rather than
// replacing the first: two waiters on one dispatch means one of them can
// never be answered, and the caller left hanging would be indistinguishable
// from a node that never reported.
func (r *Rendezvous) publish(nodeID string, attempt Attempt) (<-chan DispatchFrame, error) {
	if attempt.DispatchID == "" || nodeID == "" {
		return nil, cascade.New(cascade.KindInvalidInput,
			"nodes: a dispatch rendezvous needs a dispatch id and a node id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.pending[attempt.DispatchID]; busy {
		return nil, cascade.Newf(cascade.KindConflict,
			"nodes: dispatch %s is already awaiting a node report", attempt.DispatchID)
	}
	// Buffered so Report never blocks on a caller whose context has
	// already ended: the report is delivered and dropped, not deadlocked.
	waiter := make(chan DispatchFrame, 1)
	r.pending[attempt.DispatchID] = &pendingDispatch{attempt: attempt, reply: waiter}
	return waiter, nil
}

// withdraw removes a dispatch's pending entry.
func (r *Rendezvous) withdraw(dispatchID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, dispatchID)
}

// Claim hands the node the attempt it should run, once.
//
// A SECOND claim of the same attempt is refused. The node records the
// action id durably before executing (ReserveAction), but the claim is the
// earlier boundary: refusing here means a redelivered claim never reaches
// execution at all, rather than being caught after the node has already
// started setting up for it.
func (r *Rendezvous) Claim(nodeID string) (Attempt, error) {
	if nodeID == "" {
		return Attempt{}, cascade.New(cascade.KindInvalidInput,
			"nodes: a dispatch claim needs a node id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pending {
		if p.attempt.NodeID != nodeID || p.claimed {
			continue
		}
		p.claimed = true
		return p.attempt, nil
	}
	return Attempt{}, cascade.Newf(cascade.KindNotFound,
		"nodes: no dispatch is waiting for node %s", nodeID)
}

// Report delivers the node's signed frame to the waiting controller.
//
// The frame is NOT verified here — VerifyDispatchFrame runs on the ship
// leg, against the device record and the sequence store that only the
// controller holds. This function's job is matching, and it refuses every
// frame it cannot match rather than dropping it.
func (r *Rendezvous) Report(frame DispatchFrame) error {
	r.mu.Lock()
	pending, waiting := r.pending[frame.DispatchID]
	if !waiting {
		r.mu.Unlock()
		return cascade.Newf(cascade.KindNotFound,
			"nodes: no controller is awaiting a report for dispatch %s", frame.DispatchID)
	}
	attempt, reply := pending.attempt, pending.reply
	r.mu.Unlock()

	if frame.Attempt != attempt.Attempt {
		return ErrStaleAttemptf(frame.DispatchID, frame.Attempt, attempt.Attempt)
	}
	if frame.NodeID != attempt.NodeID {
		return cascade.Newf(cascade.KindPermissionDenied,
			"nodes: dispatch %s was placed on node %s but node %s reported it",
			frame.DispatchID, attempt.NodeID, frame.NodeID)
	}
	select {
	case reply <- frame:
		return nil
	default:
		return cascade.Newf(cascade.KindConflict,
			"nodes: dispatch %s attempt %d was already reported", frame.DispatchID, frame.Attempt)
	}
}
