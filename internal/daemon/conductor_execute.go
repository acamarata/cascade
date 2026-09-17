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
//
// routerOpts are the optional router collaborators the composition root
// built (today: conductor.NodePlacement, the P1-E17-W4-S37-T1 K×Q seam).
// Variadic so the callers that have none — every test harness here — name
// none.
func RegisterConductorExecuteHandler(
	registry *rpc.Registry,
	manifest *Manifest,
	reg provider.ProviderRegistryReader,
	quota conductor.QuotaSpiller,
	resolver conductor.ProviderResolver,
	auditWriter audit.Writer,
	clock conductor.Clock,
	security ConductorSecurity,
	accounting ConductorAccounting,
	routerOpts ...conductor.RouterOption,
) error {
	router, err := manifest.RegisterConductorRouter(reg, quota, clock, routerOpts...)
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
	accounting.wire(exec)
	manifest.Started(conductorExecutorSubsystem, "executor constructed; "+accounting.summary())
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

// ConductorAccounting carries the three write seams every dispatch's
// usage is recorded through, built by the composition root because all
// three need a database this package does not open.
//
// ALL THREE OR NONE, and the manifest says which. They were seams with no
// production caller for the whole of W-3 and W-4: the executor was built,
// `attributeUsage` ran, and it wrote through two nil hooks and a nil
// estimator, so `cascade provider usage` reported an empty table on a
// machine that had been dispatching (R-14.283). Nothing was degraded and
// nothing warned — the number was simply absent and read as zero spend.
//
// Wiring two of the three would be worse than wiring none: a recorded row
// with a confidently wrong cost is a number an operator believes, where
// an absent row at least prompts the question.
type ConductorAccounting struct {
	// Store writes one jobs_usage row per dispatch.
	Store conductor.UsageRecorder
	// Aggregator increments the per-provider counters `cascade provider
	// usage` reads.
	Aggregator conductor.UsageAggregator
	// Cost prices a dispatch from the registry's rate card.
	Cost conductor.CostEstimator
}

// wire attaches whatever was supplied. Each setter is nil-safe by its own
// contract, so a partially built ConductorAccounting degrades exactly as
// far as it should rather than panicking at the first dispatch.
func (a ConductorAccounting) wire(exec *conductor.Executor) {
	exec.SetUsageStore(a.Store).SetUsageAggregator(a.Aggregator).SetCostEstimator(a.Cost)
}

// summary is the manifest line's accounting clause, so `cascade status`
// shows whether dispatches are being counted.
//
// It exists because the failure mode here is SILENT: an unwired accounting
// path produces no error, no warning and no degraded subsystem — only an
// empty report much later. A status line that says so is the difference
// between a regression somebody notices and one somebody discovers during
// a spend review.
func (a ConductorAccounting) summary() string {
	switch {
	case a.Store == nil && a.Aggregator == nil:
		return "usage accounting NOT wired (dispatches are not counted)"
	case a.Store == nil || a.Aggregator == nil || a.Cost == nil:
		return "usage accounting partially wired (some dispatch facts are not counted)"
	default:
		return "usage accounting wired"
	}
}
