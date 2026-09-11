package wasm

import (
	"context"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: per-instance resource limits enforced on every Dispatch call:
//
//	a wazero-native memory-page ceiling, a Go-side fuel (host-ABI-call
//	count) budget, and a wall-clock deadline. Every exhaustion path
//	returns a structured typed error and terminates the module instance;
//	none ever panics or silently truncates output (task 3, acceptance
//	criterion 5).
//
// Constraints: wazero's public API (v1.12.0) has no wasmtime-style
//
//	instruction-fuel primitive, and its experimental FunctionListener
//	hook (tried first) turned out unusable for this: listeners are
//	captured once at CompileModule time from module-defined functions
//	only, so a per-Dispatch-call, per-host-call budget cannot be wired
//	through it (a real finding from building this, not a preference).
//	The reliable, available chokepoint is the one this package already
//	owns: every host-ABI call passes through dispatchFromCtx
//	(runtime.go), so fuel is counted there directly against callState.
//	Crossing budget both fails that call in-band AND cancels the shared
//	context, so the next iteration of a guest loop that keeps calling
//	observes cancellation too (and, independently, wazero's own
//	WithCloseOnContextDone inserts a cancellation check at every loop
//	header, so even a guest loop with no further host calls terminates).
//	Memory is bounded by wazero's own WithMemoryLimitPages, a real
//	RUNTIME-WIDE engine ceiling (see runtime.go's NewRuntime doc) wazero
//	enforces by refusing memory.grow, not a Go-side estimate. Wall-clock
//	uses context.WithTimeoutCause exactly as the task specifies.

// ResourceLimits bounds one Dispatch call. The zero value is invalid;
// every field must be set to a positive value (DefaultResourceLimits
// gives sane defaults).
type ResourceLimits struct {
	// MemoryPages is the maximum linear-memory page count (64KiB each)
	// a Dispatch call may use; validated against the Runtime's own
	// WithMemoryLimitPages ceiling (NewRuntime) before instantiation.
	MemoryPages uint32
	// FuelBudget is the maximum number of host-ABI calls (host_log,
	// host_http, ...) permitted during one Dispatch call. Exceeding it
	// fails the exceeding call and cancels the rest of the guest's
	// execution.
	FuelBudget uint64
	// WallClock is the maximum wall-clock duration one Dispatch call may
	// run. Exceeding it cancels the call's context.
	WallClock time.Duration
}

// DefaultResourceLimits returns a conservative, always-safe default: 16
// pages (1MiB), a 100000-call fuel budget, and a 5-second wall clock.
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{MemoryPages: 16, FuelBudget: 100000, WallClock: 5 * time.Second}
}

// Sentinel errors for each exhaustion path. Each wraps exactly one frozen
// pkg/cascade Kind (R-14.2).
var (
	// ErrMemoryLimitExceeded reports a guest memory.grow beyond the
	// Runtime's configured memory-page ceiling.
	ErrMemoryLimitExceeded = errors.New("wasm: module exceeded its memory-page limit")
	// ErrFuelExhausted reports a guest that made more host-ABI calls than
	// ResourceLimits.FuelBudget permits.
	ErrFuelExhausted = errors.New("wasm: module exhausted its fuel (host-call) budget")
	// ErrWallClockExceeded reports a Dispatch call that exceeded
	// ResourceLimits.WallClock.
	ErrWallClockExceeded = errors.New("wasm: module exceeded its wall-clock budget")
)

func wrapMemoryLimit(pages uint32) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrMemoryLimitExceeded,
		"wasm: module instance exceeded its %d-page memory limit", pages)
}

func wrapFuelExhausted(budget uint64) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrFuelExhausted,
		"wasm: module instance exhausted its %d-call fuel budget", budget)
}

func wrapWallClockExceeded(d time.Duration) error {
	return cascade.Wrapf(cascade.KindTimeout, ErrWallClockExceeded,
		"wasm: module instance exceeded its %s wall-clock budget", d)
}

// withResourceLimits returns a context carrying the wall-clock deadline,
// the resulting cancel func the caller must defer, and a separate
// CancelCauseFunc callState wires up as its fuel-exhaustion trigger
// (spendFuel below calls it directly, in-band, rather than through any
// wazero-level hook).
func withResourceLimits(ctx context.Context, limits ResourceLimits) (context.Context, context.CancelFunc, context.CancelCauseFunc) {
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, limits.WallClock, wrapWallClockExceeded(limits.WallClock))
	ctx, cancelFuel := context.WithCancelCause(ctx)
	return ctx, func() {
		cancelFuel(nil)
		cancelTimeout()
	}, cancelFuel
}

// spendFuel increments cs's fuel counter and reports ErrFuelExhausted
// (also canceling cs's shared context) once the budget is crossed. A
// callState with a nil fuelCancel (never built via withResourceLimits)
// treats fuel as unlimited — used only by tests that bypass Dispatch.
func (cs *callState) spendFuel() error {
	if cs.fuelCancel == nil {
		return nil
	}
	cs.fuelSpent++
	if cs.fuelSpent <= cs.fuelBudget {
		return nil
	}
	err := wrapFuelExhausted(cs.fuelBudget)
	cs.fuelCancel(err)
	return err
}

// mapGrowResult classifies a guest's memory.grow return value: -1 means
// wazero's engine-level WithMemoryLimitPages ceiling refused the
// request (standard WASM semantics: the guest sees a plain -1, not a
// trap), which this maps to the typed ErrMemoryLimitExceeded rather
// than leaving callers to special-case a bare -1 themselves.
func mapGrowResult(result int32, limits ResourceLimits) error {
	if result < 0 {
		return wrapMemoryLimit(limits.MemoryPages)
	}
	return nil
}

// classifyLimitErr maps a Dispatch-call error to the specific typed
// resource-limit error the context's cancellation cause names, if any;
// otherwise it returns err unchanged.
func classifyLimitErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return err
}
