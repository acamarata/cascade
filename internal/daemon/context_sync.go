package daemon

// Purpose: registers context.sync (E/S-09.T4) on the daemon's RPC router.
//   Split out of context_assemble.go (its sibling S-09.T2 registration
//   file) for the same 300-line-cap reason that file's own doc comment
//   names, and because this method needs no SQLite connection at all: like
//   context.show, it runs the S-08 discover+merge pipeline over a
//   caller-supplied cwd and nothing else — the daemon process's own home
//   directory doubles as the caller's home directory since this is a
//   local-first, single-user daemon, matching discoverMerged's own
//   os.UserHomeDir convention.
// Inputs: the daemon's shared *rpc.Registry, plus runtime.PathProvider and
//   runtime.Clock for signature symmetry with every sibling
//   registerXHandler registerContextEngineHandlers calls — unused here,
//   since regeneration needs neither a data directory nor a clock.
// Outputs: context.sync, delegating to internal/context.Sync — the SAME
//   function cmd/cascade's embedded fallback calls (context_cmd.go), so
//   the RPC and embedded paths can never disagree about what "stale"
//   means or what a regenerate run writes.
// Constraints: no store, no clock. A malformed request or a generator
//   failure returns the typed error from internal/context.Sync unchanged;
//   this file adds nothing of its own beyond request decoding.
// SPORT: internal/daemon (ADD, context.sync registration, E/S-09.T4).

import (
	"context"
	"encoding/json"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ContextSyncMethod is the context.sync JSON-RPC method name (E/S-09.T4),
// matching ContextSliceMethod's established cross-reference pattern: the
// client dials this exact literal via Client.Do (context_cmd.go).
const ContextSyncMethod = "context.sync"

// ContextSyncParams is context.sync's wire params. Cwd is always
// caller-supplied, for the same reason ContextAssembleParams.Cwd is
// (context_assemble.go's own doc comment): the daemon's own working
// directory is meaningless here.
type ContextSyncParams struct {
	Cwd       string `json:"cwd"`
	CheckOnly bool   `json:"check_only,omitempty"`
}

// ContextSyncResult is context.sync's wire result, flattened from
// internal/context.SyncResult.
type ContextSyncResult struct {
	Drift        []cascadecontext.DriftResult `json:"drift,omitempty"`
	Files        []cascadecontext.WriteResult `json:"files,omitempty"`
	Regenerated  int                          `json:"regenerated"`
	AlreadyFresh int                          `json:"already_fresh"`
}

// RegisterContextSyncHandler registers ContextSyncMethod against registry.
// paths and clock are accepted (unused) to keep this function's signature
// identical to every sibling registerXHandler registerContextEngineHandlers
// calls, rather than making that call site special-case the one handler
// with no store.
func RegisterContextSyncHandler(registry *rpc.Registry, _ runtime.PathProvider, _ runtime.Clock) error {
	registry.Register(ContextSyncMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeContextSyncParams(raw)
		if err != nil {
			return nil, err
		}
		return ComputeContextSync(ctx, p)
	})
	return nil
}

// decodeContextSyncParams decodes raw into ContextSyncParams, failing
// closed on malformed input.
func decodeContextSyncParams(raw json.RawMessage) (ContextSyncParams, error) {
	var p ContextSyncParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ContextSyncParams{}, cascade.Wrap(cascade.KindInvalidInput, err, "daemon: context.sync: malformed params")
		}
	}
	return p, nil
}

// ComputeContextSync runs internal/context.Sync for p.Cwd. Shared by the
// RPC handler above and cmd/cascade's embedded fallback (context_cmd.go),
// exactly as ComputeContextShow is shared by both context_assemble.go
// composition roots.
func ComputeContextSync(ctx context.Context, p ContextSyncParams) (ContextSyncResult, error) {
	sr, err := cascadecontext.Sync(ctx, p.Cwd, nil, p.CheckOnly)
	result := ContextSyncResult{
		Drift: sr.Drift, Files: sr.Files, Regenerated: sr.Regenerated, AlreadyFresh: sr.AlreadyFresh,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}
