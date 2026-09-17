package daemon

// Purpose: the context.harness_* RPC namespace (P1-E16-W4-S35-T3) — the
//   daemon side of `cascade context harness list` and
//   `cascade context harness sync`.
// Inputs: the daemon's shared *rpc.Registry; a cwd from the caller.
// Outputs: one HarnessState per supported harness (list), or the same
//   SyncResult context.sync produces (sync).
// Constraints: harness_sync is the SAME computation context.sync
//   performs, called through the same function. It is a second NAME for
//   one operation, not a second implementation: 07's command tree folds
//   harness under context, and the older `context sync` spelling stays
//   because scripts use it.
// SPORT: internal/daemon (ADD, context.harness_list/sync) — P1-E16-W4-S35-T3.

import (
	"context"
	"encoding/json"
	"os"
	goruntime "runtime"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The two method names.
const (
	// ContextHarnessListMethod reports every supported harness's state.
	ContextHarnessListMethod = "context.harness_list"
	// ContextHarnessSyncMethod regenerates, or drift-checks, the
	// instruction files for this working directory.
	ContextHarnessSyncMethod = "context.harness_sync"
)

// ContextHarnessListParams is context.harness_list's wire params.
//
// Cwd is caller-supplied for the same reason every other context method's
// is: the daemon's own working directory is meaningless to a client
// asking about a project.
type ContextHarnessListParams struct {
	Cwd string `json:"cwd"`
}

// ContextHarnessListResult is the method's result.
type ContextHarnessListResult struct {
	Harnesses []cascadecontext.HarnessState `json:"harnesses"`
}

// RegisterContextHarnessHandlers binds both methods.
func RegisterContextHarnessHandlers(registry *rpc.Registry, _ runtime.PathProvider, _ runtime.Clock) error {
	registry.Register(ContextHarnessListMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p ContextHarnessListParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "context.harness_list: decode params")
			}
		}
		return ComputeContextHarnessList(ctx, p)
	})
	registry.Register(ContextHarnessSyncMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeContextSyncParams(raw)
		if err != nil {
			return nil, err
		}
		return ComputeContextSync(ctx, p)
	})
	return nil
}

// ComputeContextHarnessList detects every supported harness and fills in
// each detected one's drift.
//
// Shared by the RPC handler above and cmd/cascade's embedded fallback,
// exactly as ComputeContextSlice is shared by both of context.slice's
// composition roots.
//
// A DRIFT check that fails does not fail the listing. Detection and drift
// answer different questions — "is it installed" and "is what we generate
// for it current" — and an operator whose retrieval tree is broken still
// needs the first answer. The drift columns are simply left unset, which
// is what they mean when nothing measured them.
func ComputeContextHarnessList(ctx context.Context, p ContextHarnessListParams) (ContextHarnessListResult, error) {
	if p.Cwd == "" {
		return ContextHarnessListResult{}, cascade.New(cascade.KindInvalidInput,
			"context.harness_list: cwd must not be empty")
	}
	detector := cascadecontext.NewPathDetector(goruntime.GOOS, os.Getenv, pathExists).WithFileReader(os.ReadFile)
	states, err := detector.Detect(ctx)
	if err != nil {
		return ContextHarnessListResult{}, err
	}
	if drift, derr := cascadecontext.Sync(ctx, p.Cwd, os.UserHomeDir, true); derr == nil {
		states = cascadecontext.WithDrift(states, drift)
	}
	return ContextHarnessListResult{Harnesses: states}, nil
}

// pathExists is the production PathProbe: one stat, no read.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
