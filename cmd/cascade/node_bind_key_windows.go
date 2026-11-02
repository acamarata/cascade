//go:build windows

// Purpose: windows counterpart of node_bind_key_unix.go. Windows is node
// tier-2: there is no installation key to pin, and every node verb refuses
// before this is reached.
// SPORT: cmd/cascade controllerPubKeyB64/ADDED (P1-CI-13).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

func controllerPubKeyB64(context.Context, runtime.PathProvider) (string, error) {
	return "", nodes.RefuseOnGOOS("windows")
}
