// Purpose: the owner-only notify.stats RPC and the Service surface it reads:
//
//	Stats (per-reason drop counters plus the accumulation depth) and
//	SetDropSink (the one sink every later drop is reported to).
//
// Inputs: the Service's queue set (drop counter and accumulation buffer);
//
//	the transport-verified principal.Caller in the request context.
//
// Outputs: Stats values; the "notify.stats" handler and its access
//
//	declaration.
//
// Constraints: counters and a depth only, never a notification body or
//
//	payload; a principal caller is refused KindPermissionDenied even when the
//	registry's access policy is off; SetDropSink installs a sink once.

package notify

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// StatsMethod is the RPC method that returns the Service's Stats.
const StatsMethod = "notify.stats"

var _ = rpc.DeclareAccess(StatsMethod, rpc.AccessOwnerOnly)

var (
	errStatsNotOwner = cascade.New(cascade.KindPermissionDenied,
		"notify: notify.stats is owner-only")
	errDropSinkSet = cascade.New(cascade.KindConflict,
		"notify: a drop sink is already installed")
)

// Stats is the notify.stats result: drops counted per reason and the number
// of Notifications held in the accumulation buffer.
type Stats struct {
	Dropped     map[string]uint64 `json:"dropped"`
	Accumulated int               `json:"accumulated"`
}

// Stats returns a snapshot of the per-reason drop counters and the
// accumulation buffer's depth.
func (s *Service) Stats() Stats {
	q := s.Dispatcher.queues
	st := Stats{Dropped: q.drops.snapshot()}
	q.accumMu.Lock()
	st.Accumulated = len(q.accumBuf)
	q.accumMu.Unlock()
	return st
}

// SetDropSink installs sink as the receiver of every later drop. It may be
// called once; a second call returns KindConflict and keeps the first sink.
func (s *Service) SetDropSink(sink DropSink) error {
	c := &s.Dispatcher.queues.drops
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink != nil {
		return errDropSinkSet
	}
	c.sink = sink
	return nil
}

// snapshot copies the per-reason counters.
func (c *dropCounter) snapshot() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.byReason))
	for k, v := range c.byReason {
		out[k] = v
	}
	return out
}

// RegisterStatsRPC registers notify.stats over svc on reg.
func RegisterStatsRPC(reg *rpc.Registry, svc *Service) error {
	if reg == nil || svc == nil {
		return cascade.New(cascade.KindInvalidInput, "notify: RegisterStatsRPC needs a registry and a service")
	}
	reg.Register(StatsMethod, func(ctx context.Context, _ json.RawMessage) (any, error) {
		c, ok := principal.CallerFrom(ctx)
		if !ok || c.Principal != nil {
			return nil, errStatsNotOwner
		}
		return svc.Stats(), nil
	})
	return nil
}
