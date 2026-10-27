// Purpose (this file): the in-process ci.run and ci.status JSON-RPC methods
// the `cascade mcp serve` tool process binds, so an MCP client can run the
// local CI gate and read recorded results through the same RunLocal and
// StatusLocal entry points `cascade ci run|status` use.
//
// Inputs: a CmdDeps, and a workDir function naming the directory the gate
// runs in.
// Outputs: two methods on the registry passed to RegisterMCPMethods.
// Constraints:
//   - NO repository parameter. The directory is the serve process's own
//     working directory; a caller cannot point the gate anywhere else, and
//     cannot supply a command (the steps come from [ci.local] only).
//   - Params are strict: an unknown field is refused as invalid input.
//   - A step's stdout and stderr are NEVER returned. A result carries each
//     step's name and exit code and nothing a step printed.
//   - The never-pay guard runs inside RunLocal before any store or step.
//   - Process-local only: these methods are registered by the MCP tool
//     process, never by the daemon, so no daemon RPC serves them.

package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// MethodRun is the JSON-RPC method that runs the local gate.
	MethodRun = "ci.run"
	// MethodStatus is the JSON-RPC method that lists recent CI results.
	MethodStatus = "ci.status"

	// defaultStatusLimit applies when ci.status is called without a limit.
	defaultStatusLimit = 10
	// maxStatusLimit is the largest page ci.status serves.
	maxStatusLimit = 50
)

// Both methods are owner-only. A process started with a caller principal
// token re-binds every access-classed method to a daemon forwarder, so such
// a process never runs the gate or reads results in-process; the daemon
// serves no ci.* method, and the call is refused there.
var (
	_ = rpc.DeclareAccess(MethodRun, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(MethodStatus, rpc.AccessOwnerOnly)
)

// runMethodParams is ci.run's wire request. Mirrors coretools'
// cascade_ci_run schema property for property, and StepFilter field for
// field (serveRun converts one to the other).
type runMethodParams struct {
	LintOnly  bool `json:"lint_only"`
	TestOnly  bool `json:"test_only"`
	BuildOnly bool `json:"build_only"`
}

// statusMethodParams is ci.status's wire request. Limit is a pointer so an
// absent limit (the default) is distinct from an explicit zero (refused).
type statusMethodParams struct {
	Limit *int `json:"limit"`
}

// runMethodStep is one executed step: its name and exit code only.
type runMethodStep struct {
	Name     string `json:"name"`
	ExitCode int    `json:"exit_code"`
}

// runMethodResult is ci.run's wire response.
type runMethodResult struct {
	RunID      int64           `json:"run_id"`
	Passed     bool            `json:"passed"`
	FailedStep string          `json:"failed_step"`
	Steps      []runMethodStep `json:"steps"`
}

// statusMethodRun is one ci.status row.
type statusMethodRun struct {
	RunID      int64  `json:"run_id"`
	RepoID     int64  `json:"repo_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Source     string `json:"source"`
	UpdatedAt  string `json:"updated_at"`
}

// statusMethodResult is ci.status's wire response.
type statusMethodResult struct {
	Runs []statusMethodRun `json:"runs"`
}

// RegisterMCPMethods binds ci.run and ci.status on registry. workDir
// names the directory the gate runs in (the serve process passes
// os.Getwd); a nil workDir means os.Getwd.
func RegisterMCPMethods(registry *rpc.Registry, deps CmdDeps, workDir func() (string, error)) {
	registerMCPMethods(registry, deps, workDir, &runGate{})
}

// runGate admits one ci.run at a time per registry. The CLI never ran two
// gates at once on one machine (the second was refused by the store lock);
// the shared-store fallback would otherwise let concurrent tool calls run
// builds over each other in the same checkout. A second call is refused, not
// queued. It spans this process only: another process is a separate run.
type runGate struct{ mu sync.Mutex }

// tryAcquire takes the gate, or reports that a run is already in progress.
func (g *runGate) tryAcquire() (release func(), ok bool) {
	if !g.mu.TryLock() {
		return nil, false
	}
	return g.mu.Unlock, true
}

// registerMCPMethods is RegisterMCPMethods over an explicit gate, so a test
// can hold the gate and prove the refusal without any timing.
func registerMCPMethods(registry *rpc.Registry, deps CmdDeps, workDir func() (string, error), gate *runGate) {
	if workDir == nil {
		workDir = os.Getwd
	}
	registry.Register(MethodRun, func(ctx context.Context, raw json.RawMessage) (any, error) {
		return serveRun(ctx, deps, workDir, gate, raw)
	})
	registry.Register(MethodStatus, func(ctx context.Context, raw json.RawMessage) (any, error) {
		return serveStatus(ctx, deps, raw)
	})
}

// serveRun implements ci.run.
func serveRun(ctx context.Context, deps CmdDeps, workDir func() (string, error), gate *runGate, raw json.RawMessage) (any, error) {
	var p runMethodParams
	if err := decodeMethodParams(MethodRun, raw, &p); err != nil {
		return nil, err
	}
	release, ok := gate.tryAcquire()
	if !ok {
		return nil, cascade.New(cascade.KindConflict, "ci.run: a run is already in progress; nothing was started")
	}
	defer release()
	dir, err := workDir()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci.run: resolve the working directory")
	}
	if dir == "" {
		return nil, cascade.New(cascade.KindUnavailable, "ci.run: no working directory to run in")
	}
	only := StepFilter(p) // same three fields, in the same order, on purpose
	result, err := runLocal(ctx, deps, dir, only, openRunDepsShared)
	if err != nil {
		return nil, err
	}
	return newRunMethodResult(result), nil
}

// newRunMethodResult projects a RunResult onto the wire shape. It copies
// names and exit codes only, which is how step output stays out of it.
func newRunMethodResult(r RunResult) runMethodResult {
	steps := make([]runMethodStep, 0, len(r.Steps))
	for _, s := range r.Steps {
		steps = append(steps, runMethodStep{Name: string(s.Step.Kind), ExitCode: s.ExitCode})
	}
	return runMethodResult{RunID: r.RunID, Passed: r.Passed(), FailedStep: string(r.FailedStep), Steps: steps}
}

// serveStatus implements ci.status.
func serveStatus(ctx context.Context, deps CmdDeps, raw json.RawMessage) (any, error) {
	var p statusMethodParams
	if err := decodeMethodParams(MethodStatus, raw, &p); err != nil {
		return nil, err
	}
	limit := defaultStatusLimit
	if p.Limit != nil {
		limit = *p.Limit
	}
	if limit < 1 || limit > maxStatusLimit {
		return nil, cascade.Newf(cascade.KindInvalidInput, "ci.status: limit %d is outside 1..%d", limit, maxStatusLimit)
	}
	runs, err := StatusLocal(ctx, deps, limit)
	if err != nil {
		return nil, err
	}
	out := make([]statusMethodRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, statusMethodRun{
			RunID: r.RunID, RepoID: r.RepoID, Name: r.Name,
			Status: string(r.Status), Conclusion: string(r.Conclusion), Source: r.Source,
			UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return statusMethodResult{Runs: out}, nil
}

// decodeMethodParams strictly decodes raw into v: absent or null params
// mean the zero value, an unknown field or trailing data is invalid input.
func decodeMethodParams(method string, raw json.RawMessage, v any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "%s: decode params", method)
	}
	if _, err := dec.Token(); err != io.EOF {
		return cascade.Newf(cascade.KindInvalidInput, "%s: decode params: trailing data after the params object", method)
	}
	return nil
}
