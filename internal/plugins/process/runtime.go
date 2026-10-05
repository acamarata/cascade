//go:build !windows

// Purpose: ProcessRuntime.Launch, the trusted-tier-gated execution host
//
//	for external process-tier plugins: gate, consent warning, spawn,
//	handshake, and a supervised Handle with crash-isolated restart. The
//	windows build (runtime_windows.go) refuses instead of spawning
//	(Art.5).
//
// Inputs: a Manifest and the runtime's injected collaborators (Clock,
//
//	Stderr, egress seams, audit sink, restart policy, capability checker).
//
// Outputs: a *Handle on success, or a typed error with no process forked.
// Constraints: process spawn is NOT egress (R-21.265) — internal/plugins/
//
//	process is the os/exec importer allowlist's one normative entry
//	(internal/build/egress_allow.go); it also registers the
//	plugin-process STDIO class (H/S-16.T1) idempotently, and gates spawn
//	on trust tier, so a non-trusted manifest never reaches os/exec.
//
// SPORT: internal/plugins/process runtime (ADD) — P1-E15-W4-S31-T3; host-call dispatch (CHANGE) — P1-E15-W4-S31-T4;
// closed child env, lifetime-bound monitor (CHANGE) — P1-PLG-09.

package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultCommandFactory is the production CommandFactory: a real
// os/exec.CommandContext in its own process group (execCommander, close.go).
// internal/plugins/process is the one allowlisted os/exec importer
// (internal/build/egress_allow.go). The child's environment is exactly
// m.Env: a non-nil copy, because os/exec reads a nil Env as "inherit the
// daemon's whole environment", provider keys and principal token included.
func defaultCommandFactory(ctx context.Context, m Manifest) Commander {
	cmd := exec.CommandContext(ctx, m.Command, m.Args...)
	cmd.Env = append([]string{}, m.Env...)
	return newExecCommander(cmd)
}

// ProcessRuntime is the execution host for trusted-tier process plugins.
// The zero value is not usable for Launch until Registrar and Stderr are
// set; NewProcessRuntime fills every collaborator with a production
// default.
//
// every caller spell this "ProcessRuntime.Launch" (06-FORGE-SPEC,
// 24-BUILD-ORDER); internal/hooks/egress.EgressClass sets the precedent
// for this exemption in this same repo.
//
//nolint:revive // the stutter is deliberate: the ticket contract and
type ProcessRuntime struct {
	// Clock is the injected time source. Unused directly by Launch today
	// (context.WithTimeout governs the startup deadline); carried per
	// 02-TARGET-STRUCTURE.md §v1.1 for a later ticket that schedules
	// against it.
	Clock Clock
	// Stderr receives the pre-exec consent warning. Required: this
	// package never references os.Stderr directly (the repo-wide output
	// gate), so a caller must supply the real stream explicitly.
	Stderr io.Writer
	// Registrar is where the plugin-process egress class registers.
	// Required.
	Registrar EgressRegistrar
	// Interceptor is the egress firewall stdio substitution transits.
	// Required for InterceptStdio; Launch itself does not call it.
	Interceptor EgressInterceptor
	// Restart bounds crash-isolation restarts. Zero value resolves to
	// DefaultRestartPolicy.
	Restart RestartPolicy
	// Audit receives crash reports. Nil is valid: reports are simply not
	// recorded.
	Audit AuditSink
	// StartupTimeout bounds spawn-through-handshake. Zero uses
	// DefaultCallTimeout.
	StartupTimeout    time.Duration
	CapabilityChecker HostCapabilityChecker // plugin capability boundary (hostcalls.go); nil is unwired
	// StopGrace is how long Handle.Close waits after SIGTERM before it
	// SIGKILLs the plugin's process group. Zero uses DefaultStopGrace.
	StopGrace time.Duration
	// commandFactory builds the Commander Launch starts. Defaults to
	// defaultCommandFactory; a test overrides it.
	commandFactory CommandFactory

	registerOnce sync.Once
	registerErr  error
}

// Clock abstracts time.Now, duck-typed to internal/runtime's and
// internal/testkit's Clock interfaces (both declare only
// Now() time.Time), matching internal/plugins/registry.go's pattern.
type Clock interface {
	Now() time.Time
}

// NewProcessRuntime builds a ProcessRuntime with the production command
// factory. Callers still must set Stderr and Registrar (and Interceptor,
// to use InterceptStdio) before calling Launch.
func NewProcessRuntime() *ProcessRuntime {
	return &ProcessRuntime{commandFactory: defaultCommandFactory}
}

// factory returns rt's command factory, defaulting to production.
func (rt *ProcessRuntime) factory() CommandFactory {
	if rt.commandFactory != nil {
		return rt.commandFactory
	}
	return defaultCommandFactory
}

// registerEgressClassOnce registers EgressClassPluginProcess exactly
// once per ProcessRuntime, treating ErrDuplicateClass as success: a
// second ProcessRuntime (or a second call before sync.Once fires) racing
// the same shared registry must not fail Launch merely because the class
// is already there.
func (rt *ProcessRuntime) registerEgressClassOnce(scopes []string) error {
	rt.registerOnce.Do(func() {
		rt.registerErr = registerEgressClass(rt.Registrar, scopes)
	})
	return rt.registerErr
}

// registerEgressClass registers EgressClassPluginProcess on reg. reg is
// required; a nil Registrar is a caller defect, not a silent no-op.
// Idempotency on a repeat registration is EgressRegistrar's documented
// contract, not this function's job: the composition-root adapter that
// binds reg to the real egress.Registry translates that registry's
// ErrDuplicateClass into a nil return.
func registerEgressClass(reg EgressRegistrar, scopes []string) error {
	if reg == nil {
		return cascade.New(cascade.KindInvalidInput, "process: a ProcessRuntime needs an egress Registrar")
	}
	cfg := EgressRegistrationConfig{
		Enabled:         true,
		AllowRestricted: false,
		AllowedTiers:    []SensitivityTier{TierInternal, TierPublic},
		Owner:           pluginProcessEgressOwner,
	}
	_ = scopes // declared net scopes are shown in the consent warning, not the class config
	return reg.Register(EgressClassPluginProcess, cfg)
}

// Launch validates manifest's trust tier, warns to Stderr, spawns the
// process, and negotiates the handshake. No Commander is built for a
// non-trusted manifest.
func (rt *ProcessRuntime) Launch(ctx context.Context, manifest Manifest) (*Handle, error) {
	if manifest.TrustTier != TrustTierTrusted {
		return nil, wrapUntrusted(manifest.Name, manifest.TrustTier)
	}
	if rt.Stderr == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "process: a ProcessRuntime needs a Stderr writer")
	}
	if err := rt.registerEgressClassOnce(manifest.NetScopes); err != nil {
		return nil, err
	}
	emitConsentWarning(rt.Stderr, manifest)

	startupCtx, cancel := context.WithTimeout(ctx, rt.resolvedStartupTimeout())
	defer cancel()
	return rt.spawnAndHandshake(ctx, startupCtx, manifest)
}

// resolvedStartupTimeout returns rt.StartupTimeout, defaulting to
// DefaultCallTimeout.
func (rt *ProcessRuntime) resolvedStartupTimeout() time.Duration {
	if rt.StartupTimeout > 0 {
		return rt.StartupTimeout
	}
	return DefaultCallTimeout
}

// emitConsentWarning writes the pre-exec consent line: plugin name and
// declared net scopes. The write error is deliberately ignored: Stderr is
// a diagnostic sink, and a write failure on it must never abort a launch
// that has already passed the trust gate.
func emitConsentWarning(w io.Writer, m Manifest) {
	_, _ = fmt.Fprintf(w, "cascade: launching plugin %q (net scopes: %v)\n", m.Name, m.NetScopes)
}

// spawnAndHandshake starts the Commander, negotiates the handshake, and
// on success starts the crash-isolation monitor. lifetimeCtx governs how
// long the plugin may run; startupCtx bounds only the handshake. Passing
// startupCtx as the lifetime killed every plugin the instant Launch
// returned — see lifetime.go's Constraints.
func (rt *ProcessRuntime) spawnAndHandshake(lifetimeCtx, startupCtx context.Context, manifest Manifest) (*Handle, error) {
	cmd, transport, err := rt.spawnOne(lifetimeCtx, manifest)
	if err != nil {
		return nil, err
	}
	runCtx, runCancel := context.WithCancel(lifetimeCtx)
	h := &Handle{
		Manifest: manifest, transport: transport, state: &stateBox{},
		tail: newStderrTailer(64), lifetimeCtx: lifetimeCtx, runCtx: runCtx, runCancel: runCancel,
		cmd: cmd, reaped: make(chan struct{}), monitorDone: make(chan struct{}), stopGrace: rt.resolvedStopGrace(),
	}
	h.Ack, err = performHandshake(startupCtx, transport, manifest.Name, manifest.minProtocolVersion())
	if err != nil {
		runCancel()
		abandonChild(cmd, transport, h.stopGrace)
		return nil, err
	}
	go rt.monitor(cmd, manifest, h)
	go rt.consumeHostCalls(h.lifetime(), transport)
	return h, nil
}

// monitor waits for the process to exit, restarts it per rt.Restart up
// to the attempt budget, and on exhaustion marks h state-invalid. Every
// wait is bound to h.lifetime(): once Close or the lifetime's owner ends
// it, a backoff returns at once and no respawn happens. monitorDone closes
// when the loop ends, after the last child it started was reaped.
func (rt *ProcessRuntime) monitor(cmd Waiter, manifest Manifest, h *Handle) {
	defer close(h.monitorDone)
	policy := rt.Restart.resolved()
	current, reaped := cmd, h.reaped
	for {
		// The plugin's stdout must be fully read BEFORE Wait: os/exec
		// closes that pipe once Wait sees the command exit, so waiting
		// first silently discards whatever the plugin wrote on its way out
		// -- including a host call it made and is entitled to have handled.
		h.awaitReadsDone()
		// The leader is unreaped (a zombie at worst), so its pgid is
		// still reserved: this reuse-safe kill ends members that outlived it.
		if c, ok := current.(Commander); ok {
			_ = c.Signal(syscall.SIGKILL)
		}
		exitCode := waitExitCode(current)
		if reaped != nil {
			close(reaped)
		}
		if h.lifetime().Err() != nil {
			return // closed, or the owner ended the lifetime: an exit, not a crash
		}
		restarts := h.state.incrementRestart()
		final := restarts > policy.MaxAttempts
		rt.reportCrash(manifest, exitCode, restarts, final, h)
		if final {
			h.state.markInvalid()
			return
		}
		backoffSleep(h.lifetime(), policy.backoffFor(restarts))
		current, reaped = rt.respawnOrFail(manifest, h)
	}
}

// reportCrash builds and delivers one CrashReport for this iteration.
func (rt *ProcessRuntime) reportCrash(manifest Manifest, exitCode, restarts int, final bool, h *Handle) {
	if rt.Audit == nil {
		return
	}
	rt.Audit.LogCrash(CrashReport{
		PluginName: manifest.Name, ExitCode: exitCode, StderrTail: h.tail.snapshot(),
		RestartCount: restarts, Final: final,
	})
}
