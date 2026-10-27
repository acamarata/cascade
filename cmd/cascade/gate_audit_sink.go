// Purpose: where a CLI process writes its local elevation gate rows: to the
//
//	running daemon (audit.gate_append) when one answers, otherwise straight
//	into the runtime store's audit log.
//
// Inputs: the profile paths and the clock; tests inject the daemon caller
//
//	and the direct opener.
//
// Outputs: autoGateSink, a gateSink.
//
// Constraints: the mode is chosen on the first row and kept for the whole
//
//	call. A daemon that answered once and then fails refuses; it is never
//	replaced by a direct write. Only a failure to dial the daemon socket
//	(no daemon listening) selects the direct path, and the direct path can
//	never write behind a running daemon: the store admits one opener, and
//	it is opened and closed again for each row. Every
//	daemon append must be acknowledged with a committed sequence number.
//
// SPORT: cmd/cascade/vault elevation gate audit sink (P1-SEC-08).

package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gateCall performs one daemon call (client.Client.Do in production).
type gateCall func(ctx context.Context, method string, params, out any) error

// gateAppendAck is audit.gate_append's result.
type gateAppendAck struct {
	Seq uint64 `json:"seq"`
}

const (
	gateModeUnset = iota
	gateModeDaemon
	gateModeDirect
)

// autoGateSink sends gate rows to the daemon or, with none running, to the
// store directly.
type autoGateSink struct {
	paths runtime.PathProvider
	clock runtime.Clock
	// dial returns the daemon caller; nil uses the profile's socket.
	dial func() (gateCall, error)
	// openDirect opens the direct sink; nil opens the runtime store.
	openDirect func(ctx context.Context) (gateSink, error)

	mu     sync.Mutex
	mode   int
	call   gateCall
	direct gateSink
}

// AppendGateRow implements gateSink.
func (s *autoGateSink) AppendGateRow(ctx context.Context, row gateRow) (uint64, error) {
	if err := row.validate(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.mode {
	case gateModeDaemon:
		return appendViaDaemon(ctx, s.call, row)
	case gateModeDirect:
		return s.direct.AppendGateRow(ctx, row)
	}
	call, err := s.daemonCaller()
	if err != nil {
		return 0, err
	}
	seq, err := appendViaDaemon(ctx, call, row)
	if err == nil {
		s.mode, s.call = gateModeDaemon, call
		return seq, nil
	}
	if !isDialFailure(err) {
		return 0, err
	}
	direct, derr := s.openDirectSink(ctx)
	if derr != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, derr, "gate audit: no daemon answered and the store could not be opened")
	}
	s.mode, s.direct = gateModeDirect, direct
	return direct.AppendGateRow(ctx, row)
}

// appendViaDaemon sends one row and requires a committed sequence number.
func appendViaDaemon(ctx context.Context, call gateCall, row gateRow) (uint64, error) {
	var ack gateAppendAck
	if err := call(ctx, gateAppendMethod, row, &ack); err != nil {
		return 0, err
	}
	if ack.Seq == 0 {
		return 0, cascade.New(cascade.KindUnavailable, "gate audit: the daemon did not acknowledge the row")
	}
	return ack.Seq, nil
}

// isDialFailure reports a failure to reach the daemon socket at all.
func isDialFailure(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func (s *autoGateSink) daemonCaller() (gateCall, error) {
	if s.dial != nil {
		return s.dial()
	}
	settings, err := daemon.ResolveSettings(nil, s.paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(client.UnixDialer), 5*time.Second).Do, nil
}

func (s *autoGateSink) openDirectSink(ctx context.Context) (gateSink, error) {
	if s.openDirect != nil {
		return s.openDirect(ctx)
	}
	return storeGateSink{paths: s.paths, clock: s.clock}, nil
}

// storeGateSink opens the runtime store for each row and closes it again:
// the store admits one opener, so holding it would lock out the command's
// own later store work (and every later command in a long-lived process).
type storeGateSink struct {
	paths runtime.PathProvider
	clock runtime.Clock
}

// AppendGateRow implements gateSink.
func (s storeGateSink) AppendGateRow(ctx context.Context, row gateRow) (uint64, error) {
	store, closeStore, err := openPluginStore(ctx, s.paths, s.clock)
	if err != nil {
		return 0, err
	}
	defer closeStore()
	return openGateAuditLog(store, s.clock, nil).AppendGateRow(ctx, row)
}
