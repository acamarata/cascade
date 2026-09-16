package daemon

// Purpose: registers "conductor.execute" (R-16.80) - the JSON-RPC method
//   cmd/cascade/run_exec.go:132's fetchRun dials - against the daemon's
//   real *rpc.Registry, and calls Manifest.RegisterConductorRouter for
//   real at daemon startup (R-16.80 Ruling 2). Neither symbol had a
//   production caller before this file: `cascade run` dialed a method
//   nobody answered.
// Inputs: the daemon's shared *rpc.Registry and Manifest, a real
//   provider.ProviderRegistryReader and conductor.QuotaSpiller, a
//   conductor.ProviderResolver (cmd/cascade/daemon_unix_conductor.go
//   passes internal/providers/dispatch.NewResolver's real implementation,
//   see DEFECT-conductor-execute-permanently-unavailable.md), a real
//   audit.Writer, and the shared conductor.Clock.
// Outputs: a registered "conductor.execute" handler that always answers
//   for real - never method-not-found - plus a real *conductor.
//   DefaultRouter recorded on Manifest under "conductor.router" and a
//   real-or-failed *conductor.Executor recorded under
//   "conductor.executor". Once construction succeeds, "job.cancel" is
//   also registered against the same real *Executor
//   (conductor.RegisterHandlers), retiring internal/build/testonly-
//   allow.json's entry for that symbol.
// Constraints: never registers a fabricated Resolver. A handler that
//   always errors dressed up as a working seam is exactly the shortcut
//   Art.1 forbids. A nil resolver argument still answers NewExecutor's
//   own real ErrConstructionFailed (a genuine typed error distinct from
//   method-not-found) rather than panicking, but the daemon composition
//   root no longer passes nil: it passes a real resolver whose own
//   CredentialSource seam is unwired pending an owner decision on daemon
//   credential custody (see that resolver's own doc comment), so a
//   key-authenticated dispatch today fails closed per-call, inside
//   Resolve, rather than once at construction time.
// SPORT: internal/daemon (ADD, R-16.80; CHANGE, DEFECT-conductor-execute-
//   permanently-unavailable.md).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ConductorExecuteMethod is the JSON-RPC method name cmd/cascade/
// run_exec.go:132's fetchRun dials.
const ConductorExecuteMethod = "conductor.execute"

// ConductorSecurity carries the five R-21.206 security-pipeline
// collaborators from the composition root to NewExecutor.
//
// They travel as a struct rather than as five more positional parameters
// because the constructor already takes seven, and because
// Pipeline.Ready() treats them as ONE thing: all five present, or every
// call refused. A zero ConductorSecurity therefore produces exactly the
// pre-existing "security pipeline not ready" behaviour — the composition
// root can still bring the door up before they are built, which is what it
// did for the whole of W-3 (see the W-3 gate report, Art.9).
type ConductorSecurity struct {
	Classifier  conductor.Classifier
	Taxonomy    conductor.TaskClassTable
	Policy      conductor.PolicyEvaluator
	Sensitivity conductor.SensitivityGate
	Firewall    *egress.Engine
}

// conductorExecutorSubsystem is the fail-loud Manifest name this file's
// NewExecutor attempt reports under (R-14.87), distinct from
// RegisterConductorRouter's own "conductor.router" name.
const conductorExecutorSubsystem = "conductor.executor"

// RegisterConductorExecuteHandler is the daemon composition root's call
// site (R-16.80 Ruling 2) for both Manifest.RegisterConductorRouter and
// the "conductor.execute" JSON-RPC method. It always registers the
// method - a client always gets a real answer, never method-not-found -
// but what that answer IS depends on whether NewExecutor could actually
// build an Executor from the collaborators supplied.
func RegisterConductorExecuteHandler(
	registry *rpc.Registry,
	manifest *Manifest,
	reg provider.ProviderRegistryReader,
	quota conductor.QuotaSpiller,
	resolver conductor.ProviderResolver,
	auditWriter audit.Writer,
	clock conductor.Clock,
	security ConductorSecurity,
) error {
	router, err := manifest.RegisterConductorRouter(reg, quota, clock)
	if err != nil {
		return err
	}
	manifest.Register(conductorExecutorSubsystem)
	exec, cerr := conductor.NewExecutor(conductor.ExecutorConfig{
		Router: router, Resolver: resolver, Audit: auditWriter, Clock: clock,
		Classifier:  security.Classifier,
		Taxonomy:    security.Taxonomy,
		Policy:      security.Policy,
		Sensitivity: security.Sensitivity,
		Firewall:    security.Firewall,
	})
	if cerr != nil {
		manifest.Failed(conductorExecutorSubsystem, cerr.Error())
		registry.Register(ConductorExecuteMethod, conductorExecuteUnavailableHandler(cerr))
		return nil
	}
	manifest.Started(conductorExecutorSubsystem, "executor constructed")
	registry.Register(ConductorExecuteMethod, conductorExecuteHandler(exec))
	conductor.RegisterHandlers(registry, exec)
	return nil
}

// conductorExecuteUnavailableHandler answers every "conductor.execute"
// call with the real construction failure recorded once at startup: a
// genuine, typed, non-method-not-found error, never a fabricated result.
func conductorExecuteUnavailableHandler(cerr error) rpc.HandlerFunc {
	return func(context.Context, json.RawMessage) (any, error) {
		return nil, cascade.Wrap(cascade.KindUnavailable, cerr, "conductor.execute: executor unavailable")
	}
}

// conductorExecuteHandler decodes the wire params cmd/cascade/run.go:169
// (runRequestParams) freezes into a provider.ModelRequest and dispatches
// it through exec.Execute. The wire shape is duplicated here rather than
// imported: cmd/cascade is package main and this package cannot import it
// (mirrors internal/conductor/cancel.go's jobCancelParams, decoded
// independently of pkg/provider.Client's own wrapper types for the same
// reason).
//
// KNOWN GAP, disclosed rather than papered over: a params.FanOut > 1
// request is not routed through exec.ExecuteFanOut here - that call needs
// a WithPermitFn/JournalAppender this composition root does not build yet
// (a separate, larger gap than R-16.80's connector fix). It is dispatched
// as an ordinary single Execute, which is honest today only because
// exec.Execute itself cannot yet be reached in production (nil
// Resolver -> ErrConstructionFailed, see RegisterConductorExecuteHandler);
// wiring ExecuteFanOut is left for the ticket that builds those two
// collaborators.
func conductorExecuteHandler(exec *conductor.Executor) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var wire conductorExecuteParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &wire); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "conductor.execute: decode params")
			}
		}
		req, err := wire.toModelRequest()
		if err != nil {
			return nil, err
		}
		return exec.Execute(ctx, req)
	}
}
