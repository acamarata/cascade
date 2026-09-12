// Purpose: the R-21.206 six-collaborator security-pipeline readiness gate.
//   NewExecutor is the sole constructor for *Executor; Pipeline.Ready is the
//   gate execute.go's Execute and ExecuteStream call first, before any
//   validation or dispatch. execCapability is the unexported raw execution
//   seam pipeline.go issues to execute.go only after Ready passes, so no
//   package outside conductor can ever name or hold a raw
//   provider.ModelProvider (R-21.206 B).
// Inputs: an ExecutorConfig naming every collaborator.
// Outputs: a constructed *Executor, or ErrConstructionFailed.
// Constraints: construction fails immediately (ErrConstructionFailed) when
//   Router, Resolver, Audit or Clock is nil - there is no default, no no-op
//   writer, and no build-tag bypass for the audit broker specifically
//   (R-21.206 A). The remaining five of the six security collaborators
//   (Classifier, Taxonomy, Policy, Sensitivity, Firewall) may be nil at
//   construction, so a daemon can bring the door up before every
//   collaborator is wired; Ready() gates each call until all six are
//   present, per TestExecute_SecurityPipelineNotReady removing them one at
//   a time and asserting zero provider calls in every case.
// SPORT: conductor.pipeline/ADD (P1-E11-W3-S22-T1).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// execCapability is the unexported raw execution capability. It is a func
// type, never an exported interface, so no caller outside this package can
// name it; only Pipeline.capability constructs one.
type execCapability func(ctx context.Context, sel provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error)

// ExecutorConfig collects every constructor-time collaborator NewExecutor
// requires. Router and Resolver are the purely-injected dispatch seams
// (R-21.217 E); the remaining six fields are the R-21.206 A security
// pipeline.
type ExecutorConfig struct {
	Router      Router
	Resolver    ProviderResolver
	Classifier  Classifier
	Taxonomy    TaskClassTable
	Policy      PolicyEvaluator
	Audit       audit.Writer
	Sensitivity SensitivityGate
	Firewall    *egress.Engine
	Clock       runtime.Clock
}

// Pipeline holds the security pipeline's readiness state. The zero value
// is not ready; build one with newPipeline.
type Pipeline struct {
	cfg ExecutorConfig
}

func newPipeline(cfg ExecutorConfig) *Pipeline {
	return &Pipeline{cfg: cfg}
}

// Ready reports whether all six R-21.206 collaborators are installed. A
// nil Classifier, Taxonomy, Policy, Audit, Sensitivity or Firewall each
// independently refuses.
func (p *Pipeline) Ready() error {
	switch {
	case p.cfg.Classifier == nil, p.cfg.Taxonomy == nil, p.cfg.Policy == nil,
		p.cfg.Audit == nil, p.cfg.Sensitivity == nil, p.cfg.Firewall == nil:
		return ErrSecurityPipelineNotReady
	}
	return nil
}

// capability returns the unexported execCapability, dispatching through
// the injected ProviderResolver. execute.go must call Ready() itself
// before ever invoking the returned function; capability performs no
// readiness check of its own.
func (p *Pipeline) capability() execCapability {
	resolver := p.cfg.Resolver
	return func(ctx context.Context, sel provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error) {
		driver, err := resolver.Resolve(ctx, sel)
		if err != nil {
			return provider.ChatResponse{}, err
		}
		return driver.Chat(ctx, req)
	}
}

// embedDispatch is the unexported raw embed-execution capability: the
// Embed-verb counterpart to execCapability. It is a distinct type, not a
// reuse of execCapability, because provider.ModelProvider.Chat and .Embed
// take unrelated request/response shapes (R-40.X10).
type embedDispatch func(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error)

// embedCapability returns the unexported embedDispatch, dispatching
// through the injected ProviderResolver exactly like capability() does for
// Chat. It is embed.go's Executor.Embed's sole permitted entry into
// pkg/provider.ModelProvider.Embed (R-40.X10): callgraph_test.go's allowed
// set names Pipeline.embedCapability alongside Pipeline.capability, and no
// other function in the module may call this verb.
func (p *Pipeline) embedCapability() embedDispatch {
	resolver := p.cfg.Resolver
	return func(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		driver, err := resolver.Resolve(ctx, sel)
		if err != nil {
			return provider.ModelEmbedResponse{}, err
		}
		return driver.Embed(ctx, req)
	}
}

// countDispatch is the unexported raw count-execution capability: the
// Count-verb counterpart to execCapability/embedDispatch, added by
// FIX-manifest-collision-and-conductor-seam for the same reason
// embedDispatch was added under R-40.X10 - providers/agents/local needs a
// sel-resolved leaf-dispatch path to ModelProvider.Count that never
// requires it to hold a raw ModelProvider itself.
type countDispatch func(ctx context.Context, sel provider.Selection, req provider.CountRequest) (provider.CountResponse, error)

// countCapability returns the unexported countDispatch, dispatching
// through the injected ProviderResolver exactly like capability() and
// embedCapability() do for Chat/Embed. It is count.go's Executor.Count's
// sole permitted entry into pkg/provider.ModelProvider.Count.
func (p *Pipeline) countCapability() countDispatch {
	resolver := p.cfg.Resolver
	return func(ctx context.Context, sel provider.Selection, req provider.CountRequest) (provider.CountResponse, error) {
		driver, err := resolver.Resolve(ctx, sel)
		if err != nil {
			return provider.CountResponse{}, err
		}
		return driver.Count(ctx, req)
	}
}

// streamDispatch is the unexported raw stream-execution capability: the
// Stream-verb counterpart to execCapability, added for the same reason as
// countDispatch above. Unlike ExecuteStream's own router-driven, channel-
// based form, this is the sel-resolved leaf form: it forwards to
// ModelProvider.Stream's own (ctx, req, sink) error shape directly, with
// no channel bridging and no CancelFunc (plain pkg/provider types only,
// so a caller outside this package can hold the matching interface with
// no adapter type needed, unlike Embed's EmbeddingExecutorFunc).
type streamDispatch func(ctx context.Context, sel provider.Selection, req provider.ChatRequest, sink provider.StreamSink) error

// streamCapability returns the unexported streamDispatch, dispatching
// through the injected ProviderResolver exactly like the other three
// leaf-dispatch capabilities. It is stream_door.go's Executor.Stream's
// sole permitted entry into pkg/provider.ModelProvider.Stream outside
// ExecuteStream's own router-driven path.
func (p *Pipeline) streamCapability() streamDispatch {
	resolver := p.cfg.Resolver
	return func(ctx context.Context, sel provider.Selection, req provider.ChatRequest, sink provider.StreamSink) error {
		driver, err := resolver.Resolve(ctx, sel)
		if err != nil {
			return err
		}
		return driver.Stream(ctx, req, sink)
	}
}

// NewExecutor constructs the model.execute door. It fails closed
// (ErrConstructionFailed) when Router, Resolver, Audit or Clock is nil:
// these four are mechanical requirements every configuration needs, and
// Audit specifically has no permitted default per R-21.206 A. The other
// five R-21.206 collaborators may be nil here; Pipeline.Ready gates them
// per call.
func NewExecutor(cfg ExecutorConfig) (*Executor, error) {
	if cfg.Router == nil || cfg.Resolver == nil || cfg.Audit == nil || cfg.Clock == nil {
		return nil, ErrConstructionFailed
	}
	return &Executor{pipeline: newPipeline(cfg), router: cfg.Router}, nil
}
