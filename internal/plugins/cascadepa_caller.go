package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	errToolNoCaller  = cascade.New(cascade.KindPermissionDenied, "cascade-pa tool call has no caller")
	errToolPrincipal = cascade.New(cascade.KindPermissionDenied, "cascade-pa tool call cannot forward its principal")
)

// requireToolCaller admits only registry-stamped owner views. A process token
// cannot establish that a principal supplied in a context is the same caller.
func requireToolCaller(ctx context.Context) error {
	caller, ok := principal.CallerFrom(ctx)
	if !ok {
		return errToolNoCaller
	}
	if caller.Principal != nil {
		return errToolPrincipal
	}
	return nil
}

// modelFacingClient is the construction boundary for tool adapter clients.
func modelFacingClient(paths runtime.PathProvider, dial client.DialFunc, timeout time.Duration) *client.Client {
	return client.New(paths.SocketPath(), dial, timeout).ModelFacing()
}
