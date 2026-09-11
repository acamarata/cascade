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
//	Stderr writer, egress registrar/interceptor, command factory, audit
//	sink, restart policy).
//
// Outputs: a *Handle on success, or a typed error with no process forked.
// Constraints: process spawn is NOT egress (R-21.265) — internal/plugins/
//
//	process is the os/exec importer allowlist's one normative entry
//	(internal/build/egress_allow.go), and this file registers the
//	plugin-process STDIO class separately, idempotently, via the H/S-16.T1
//	inventory. The trusted-tier gate runs before any Commander is built,
//	so a non-trusted manifest never reaches os/exec.
//
// SPORT: internal/plugins/process runtime (ADD) — P1-E15-W4-S31-T3.

package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Commander is the seam onto a started child process, satisfied by
// *exec.Cmd. Decoupling from os/exec directly lets Launch's trusted-tier
// gate be tested without an exec.Cmd ever being constructed, and lets a
// crash/restart test drive a fake process.
type Commander interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
}

// CommandFactory builds the Commander Launch starts for manifest. The
// production default builds a real *exec.Cmd; a test supplies a fake to
// assert Launch never calls it for a non-trusted manifest.
type CommandFactory func(ctx context.Context, m Manifest) Commander

// defaultCommandFactory is the production CommandFactory: a real
// os/exec.CommandContext. internal/plugins/process is the one allowlisted
// os/exec importer (internal/build/egress_allow.go).
func defaultCommandFactory(ctx context.Context, m Manifest) Commander {
	return exec.CommandContext(ctx, m.Command, m.Args...)
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
	StartupTimeout time.Duration
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
	return rt.spawnAndHandshake(startupCtx, manifest)
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
// on success starts the crash-isolation monitor.
func (rt *ProcessRuntime) spawnAndHandshake(ctx context.Context, manifest Manifest) (*Handle, error) {
	cmd, transport, err := rt.spawnOne(ctx, manifest)
	if err != nil {
		return nil, err
	}
	h := &Handle{Manifest: manifest, transport: transport, state: &stateBox{}, tail: newStderrTailer(64)}
	h.Ack, err = performHandshake(ctx, transport, manifest.Name, manifest.minProtocolVersion())
	if err != nil {
		_ = transport.Close()
		_ = cmd.Wait()
		return nil, err
	}
	go rt.monitor(cmd, manifest, h)
	return h, nil
}

// spawnOne builds a Commander from manifest, opens its stdio pipes,
// starts it, and wraps its stdio in a Transport.
func (rt *ProcessRuntime) spawnOne(ctx context.Context, manifest Manifest) (Commander, *Transport, error) {
	cmd := rt.factory()(ctx, manifest)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: opening plugin stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: opening plugin stdout")
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: starting plugin process")
	}
	return cmd, NewTransport(stdin, stdout, rt.StartupTimeout), nil
}

// monitor waits for the process to exit, restarts it per rt.Restart up
// to the attempt budget, and on exhaustion marks h state-invalid. It
// loops on the running Commander until either a restart succeeds and
// replaces h's transport, or the budget is exhausted.
func (rt *ProcessRuntime) monitor(cmd Waiter, manifest Manifest, h *Handle) {
	policy := rt.Restart.resolved()
	current := cmd
	for {
		exitCode := waitExitCode(current)
		restarts := h.state.incrementRestart()
		final := restarts > policy.MaxAttempts
		rt.reportCrash(manifest, exitCode, restarts, final, h)
		if final {
			h.state.markInvalid()
			return
		}
		backoffSleep(context.Background(), policy.backoffFor(restarts))
		next, err := rt.respawn(manifest, h)
		if err != nil {
			current = failedWaiter{}
			continue
		}
		current = next
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

// respawn attempts one relaunch: a fresh Commander, handshake, and (on
// success) an atomic swap of h's transport. The returned Commander is
// what the monitor loop waits on next.
func (rt *ProcessRuntime) respawn(manifest Manifest, h *Handle) (Commander, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rt.resolvedStartupTimeout())
	defer cancel()
	cmd, transport, err := rt.spawnOne(ctx, manifest)
	if err != nil {
		return nil, err
	}
	ack, err := performHandshake(ctx, transport, manifest.Name, manifest.minProtocolVersion())
	if err != nil {
		_ = transport.Close()
		_ = cmd.Wait()
		return nil, err
	}
	h.Ack = ack
	h.swapTransport(transport)
	return cmd, nil
}

// failedWaiter is used when a respawn attempt itself fails before a
// process could even be started: it reports an immediate exit so the
// monitor loop's restart-count accounting still advances toward the
// budget rather than spinning without ever reaching state-invalid.
type failedWaiter struct{}

// Wait reports the synthetic exit for a respawn that never started.
func (failedWaiter) Wait() error {
	return cascade.New(cascade.KindUnavailable, "process: respawn attempt failed before the process started")
}
