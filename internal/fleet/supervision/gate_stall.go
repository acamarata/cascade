package supervision

// Purpose (this file): the gate-stall action sequence Detector.Observe
// runs once per gate-denied stall (the third denial for a job inside 30
// minutes). It fails CLOSED: with no actions wired it files a Priority 0
// "gate-stall-unwired:<job>" error item, and any failing step files a
// Priority 0 "gate-stall-<step>-failed:<job>" item and stops, so no
// release or FailJob ever runs after a failed step.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gateStallScopeThreshold is the number of gate-stall items in one
// session that raises a global stall-scope item.
const gateStallScopeThreshold = 3

// gateStallHook holds the wired actions and the episodes already run.
// done is keyed by job|attempt|deniedAt-ms so a repeat signal for the
// same denial (RecordGateDenied stays true past the third) is a no-op.
type gateStallHook struct {
	mu      sync.Mutex
	store   *Store
	actions *GateStallActions
	done    map[string]bool
}

// WithGateStallActions wires the gate-stall sequence's seams. A nil
// field returns ErrGateStallActionsInvalid naming the field and leaves
// the current actions unchanged.
func (d *Detector) WithGateStallActions(a GateStallActions) error {
	fields := []struct {
		name    string
		missing bool
	}{
		{"LastDenial", a.LastDenial == nil},
		{"FailJob", a.FailJob == nil},
		{"Releases", a.Releases == nil},
		{"RetryHint", a.RetryHint == nil},
	}
	for _, f := range fields {
		if f.missing {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrGateStallActionsInvalid,
				"supervision: gate-stall actions need every field: %s", f.name)
		}
	}
	d.gate.mu.Lock()
	defer d.gate.mu.Unlock()
	d.gate.actions = &a
	return nil
}

// run executes the gate-stall sequence for jobID in sessionID. It has
// no return: every outcome is an attention item, and a Push or
// ListInScopes error simply ends the run (the ladder still advances).
func (h *gateStallHook) run(ctx context.Context, sessionID, jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref := scope.Ref{Kind: scope.ScopeKindSession, ID: sessionID}
	if h.actions == nil {
		h.push(ctx, KindError, "gate-stall-unwired:"+jobID, ref, 0)
		return
	}
	denial, err := h.actions.LastDenial(ctx, jobID)
	if err != nil {
		h.fail(ctx, "lastdenial", jobID, ref)
		return
	}
	key := fmt.Sprintf("%s|%d|%d", jobID, denial.Attempt, denial.DeniedAt.UnixMilli())
	if h.done[key] {
		return
	}
	if h.done == nil {
		h.done = make(map[string]bool)
	}
	h.done[key] = true
	hint, step := h.act(ctx, jobID, denial)
	if step != "" {
		h.fail(ctx, step, jobID, ref)
		return
	}
	src := fmt.Sprintf("gate-stall:%s attempt %d/3; next retry not before %s; reason: %s",
		jobID, denial.Attempt, denial.DeniedAt.Add(hint).UTC().Format(time.RFC3339), denial.Reason)
	if !h.push(ctx, KindStall, src, ref, 1) {
		return
	}
	h.raiseScope(ctx, sessionID, ref)
}

// act runs FailJob, the releases and RetryHint in order, returning the
// hint or the name of the first failing step (no later step runs).
func (h *gateStallHook) act(ctx context.Context, jobID string, denial GateDenial) (time.Duration, string) {
	if err := h.actions.FailJob(ctx, jobID, denial); err != nil {
		return 0, "failjob"
	}
	rel, err := h.actions.Releases(ctx, jobID)
	if err != nil {
		return 0, "releases"
	}
	for _, r := range rel {
		if r == nil {
			return 0, "releases"
		}
	}
	if err := nodes.CancelInFlight(rel...); err != nil {
		return 0, "cancel"
	}
	hint, err := h.actions.RetryHint(denial.Attempt)
	if err != nil {
		return 0, "retryhint"
	}
	return hint, ""
}

// raiseScope counts the session's gate-stall items and, at the
// threshold or above, files the global stall-scope item.
func (h *gateStallHook) raiseScope(ctx context.Context, sessionID string, ref ScopeRef) {
	kind := KindStall
	items, err := h.store.ListInScopes(ctx, []ScopeRef{ref}, Filter{KindFilter: &kind, IncludeAcked: true})
	if err != nil {
		return
	}
	n := 0
	for _, it := range items {
		if it.ScopeRef == ref && strings.HasPrefix(it.SourceRef, "gate-stall:") {
			n++
		}
	}
	if n < gateStallScopeThreshold {
		return
	}
	global := scope.Ref{Kind: scope.ScopeKindGlobal, ID: "global"}
	h.push(ctx, KindStall, fmt.Sprintf("stall-scope:session:%s:%d", sessionID, n), global, 0)
}

// fail files the Priority 0 step-failed error item.
func (h *gateStallHook) fail(ctx context.Context, step, jobID string, ref ScopeRef) {
	h.push(ctx, KindError, fmt.Sprintf("gate-stall-%s-failed:%s", step, jobID), ref, 0)
}

// push files one attention item, reporting whether it succeeded.
func (h *gateStallHook) push(ctx context.Context, kind Kind, src string, ref ScopeRef, priority int) bool {
	_, err := h.store.Push(ctx, AttentionItem{Kind: kind, SourceRef: src, ScopeRef: ref, Priority: priority})
	return err == nil
}
