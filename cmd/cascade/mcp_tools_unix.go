//go:build !windows

// Purpose: the platform half of the MCP tool filter's grant store
//
//	(P1-E16-W4-S34-T2). On every POSIX platform it opens the SAME
//	migrated cascade.db the daemon opens, so a grant made through
//	`cascade policy grant` is the grant the MCP tool filter reads.
//
// Constraints: Art.5 — the Windows sibling is an ASSERTED refusal, never
//
//	a silent skip. See mcp_tools_windows.go.
//
// SPORT: cmd/cascade/mcp (ADD) — P1-E16-W4-S34-T2.
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// openMCPPolicyStore opens the grant store the capability filter reads.
func openMCPPolicyStore(paths runtime.PathProvider, clock runtime.Clock) (provider.Store, func(), error) {
	store, _, closeStore, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		return nil, nil, err
	}
	return store, closeStore, nil
}
