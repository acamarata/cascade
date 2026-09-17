//go:build windows

// Purpose: the Windows half of the MCP tool filter's grant store
//
//	(P1-E16-W4-S34-T2) — an asserted refusal (Art.5), not a silent skip.
//
// Constraints: the whole runtime-store composition this build needs
//
//	(openRuntimeStore, newRuntimeMigrator, daemon_unix_store.go) is
//	POSIX-only at HEAD, so there is no migrated cascade.db to read grants
//	from on Windows. The honest consequence is stated here and asserted
//	by mcp_tools_windows_test.go: capability-gated MCP tools are not
//	listed on Windows. They are not listed UNGATED either — the caller
//	turns this refusal into mcp.DenyAllFilter, so the failure mode is
//	fewer tools, never an ungoverned one.
//
// SPORT: cmd/cascade/mcp (ADD) — P1-E16-W4-S34-T2.
package main

import (
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrMCPPolicyStoreUnsupported is the refusal this platform answers with.
var ErrMCPPolicyStoreUnsupported = cascade.New(cascade.KindUnsupported,
	"cascade mcp: the policy grant store is not available on windows; capability-gated tools are not listed")

// openMCPPolicyStore refuses on Windows.
func openMCPPolicyStore(runtime.PathProvider, runtime.Clock) (provider.Store, func(), error) {
	return nil, nil, ErrMCPPolicyStoreUnsupported
}
