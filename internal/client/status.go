package client

// Purpose: the Status typed method wrapper — this SDK's one concrete
//   D/S-06.T3-registered method (internal/daemon.StatusMethod, "status.get";
//   see internal/daemon/status.go's StatusProvider, the real handler
//   cmd/cascade/daemon_unix_run.go's buildRPCServer registers). cmd/cascade/
//   status.go calls this instead of assembling the request/decoding the
//   response itself (hard requirement 1). Status returns internal/daemon's
//   own StatusResponse type directly — this SDK importing internal/daemon
//   for its result shape (rather than a duplicated copy) is not the
//   boundary this ticket closes: the forbidden edge is cmd/cascade
//   importing internal/rpc (the server-SIDE dispatch package) to hand-roll
//   a call, not internal/client depending on a sibling internal/ package
//   for a plain result-type definition. internal/daemon does not import
//   internal/client back, so this adds no cycle.
//
// Note on scope (contract/tree note, see this ticket's completion report):
//   the ticket text additionally names "Daemon.Stop(), Daemon.Restart(),
//   Daemon.Version()" as minimum typed wrappers. No such JSON-RPC methods
//   are registered anywhere in the daemon (internal/daemon/status.go
//   registers only status.get; grep confirms no "daemon.stop" et al.
//   Register call exists), and `cascade daemon stop/restart/start` are
//   OS-level PID-signal operations implemented in cmd/cascade/daemon_unix.go
//   (files_scope explicitly forbids touching that file — "another agent
//   owns it right now") rather than RPC calls at all. Adding speculative
//   wrapper methods for RPC methods that do not exist on the server, with
//   no legal in-scope production caller to wire them to, would itself be
//   the R-14.166/171/175 "built, tested, called by nothing that ships"
//   pattern those rulings forbid — and this ticket cannot touch
//   internal/build/testonly-allow.json to exempt them (not in
//   files_scope). Do(ctx, method, params, out) remains the general
//   mechanism; adding Daemon.Stop/Restart/Version wrappers is mechanical
//   once a real RPC method and an in-scope caller both exist.

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon"
)

// Status calls status.get and returns the daemon's live status snapshot.
func (c *Client) Status(ctx context.Context) (daemon.StatusResponse, error) {
	var res daemon.StatusResponse
	if err := c.Do(ctx, daemon.StatusMethod, nil, &res); err != nil {
		return daemon.StatusResponse{}, err
	}
	return res, nil
}
