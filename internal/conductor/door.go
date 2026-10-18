// Purpose: the one enforcement chokepoint every model door passes through.
//   Execute, ExecuteStream, Chat, Embed, Count and Stream all call enterDoor
//   before any provider is resolved, so readiness, the resolved sensitivity
//   tier, lane admission, policy and egress substitution of the payload are
//   decided in one place and in one order. This file also owns the only
//   dispatch helpers that reach Pipeline.*Capability (TestDoorIsOnlyDispatchPath).
// Inputs: a doorCall naming the verb, the request or caller-chosen Selection,
//   and the payload to substitute.
// Outputs: a doorPass (resolved tier, canonical Selection, substituted
//   payload, flags), or a refusal that already wrote its one audit record.
// Constraints: FAIL CLOSED. A leaf door with no context tier is restricted;
//   the tier only ever narrows (JoinSensitivity); a Selection is never
//   trusted: its lane and provider are re-read from the registry, and a
//   router that cannot expose its registry refuses every leaf call. Leaf
//   doors run no classifier and no task-class step.
// SPORT: conductor.door/ADD (P1-SEC-01).

package conductor

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DoorVerb names a model door; its string is the audit Action.
type DoorVerb string

// The six model doors.
const (
	VerbExecute       DoorVerb = "model.execute"
	VerbExecuteStream DoorVerb = "model.execute_stream"
	VerbChat          DoorVerb = "model.chat"
	VerbEmbed         DoorVerb = "model.embed"
	VerbCount         DoorVerb = "model.count"
	VerbStream        DoorVerb = "model.stream"
)

type doorTierKey struct{}

// ContextWithDoorSensitivity declares the sensitivity tier of a leaf-door
// call (Chat, Embed, Count, Stream). A call without it is restricted.
func ContextWithDoorSensitivity(ctx context.Context, t provider.SensitivityTier) context.Context {
	return context.WithValue(ctx, doorTierKey{}, t)
}

// DoorSensitivityFrom returns the tier ContextWithDoorSensitivity attached.
func DoorSensitivityFrom(ctx context.Context) (provider.SensitivityTier, bool) {
	t, ok := ctx.Value(doorTierKey{}).(provider.SensitivityTier)
	return t, ok
}

// doorCall is what a door hands enterDoor: Execute verbs set Req, leaf verbs
// set Sel and Payload ([]provider.ChatMessage, []string or string).
type doorCall struct {
	Verb    DoorVerb
	Req     *provider.ModelRequest
	Sel     provider.Selection
	Model   string // the request's model name; substituted like the payload
	Payload any
}

// doorPass is what a door may dispatch: Payload is the substituted value
// (a provider.ModelRequest for Execute verbs, else the leaf payload type).
type doorPass struct {
	Tier    provider.SensitivityTier
	Sel     provider.Selection
	Model   string
	Payload any
	Flags   []string
}

// laneSource is how the door reads lane data for a caller-chosen Selection.
// *DefaultRouter implements it; a Router that does not makes leaf doors refuse.
type laneSource interface {
	doorLane(ctx context.Context, sel provider.Selection) (laneCandidate, RestrictedPermits, error)
}

// enterDoor is the chokepoint. Order: Ready, tier, lane admission (leaf) or
// Router.Select (Execute verbs), Policy.Authorize, substitution. Every
// refusal writes one audit record (RiskLevel = tier, Action = verb).
func (e *Executor) enterDoor(ctx context.Context, c doorCall) (doorPass, error) {
	if err := e.pipeline.Ready(); err != nil {
		return doorPass{}, err
	}
	var pass doorPass
	var err error
	if c.Req != nil {
		pass, err = e.passExecute(ctx, c)
	} else {
		pass, err = e.passLeaf(ctx, c)
	}
	if err != nil {
		e.auditDoor(ctx, c.Verb, pass.Tier, doorKey(c), pass.Sel, "refusal", err)
		return pass, err
	}
	return pass, nil
}

// passExecute is the Execute/ExecuteStream half: authorize resolves the tier,
// the router sees that tier, and the substituted request leaves in Payload.
func (e *Executor) passExecute(ctx context.Context, c doorCall) (doorPass, error) {
	req := *c.Req
	tier, err := e.authorize(ctx, req)
	pass := doorPass{Tier: tier}
	if err != nil {
		return pass, err
	}
	req.Sensitivity = tier
	if pass.Sel, err = e.router.Select(ctx, req); err != nil {
		return pass, selectRefusal(err)
	}
	if req, err = e.substituteInputs(ctx, req, tier); err != nil {
		pass.Flags = []string{"egress_substitution_failed"}
		return pass, ErrEgressSubstitutionFailed
	}
	pass.Payload = req
	return pass, nil
}

// passLeaf is the Chat/Embed/Count/Stream half.
func (e *Executor) passLeaf(ctx context.Context, c doorCall) (doorPass, error) {
	pass := doorPass{Tier: e.leafTier(ctx)}
	src, ok := e.router.(laneSource)
	if !ok {
		return pass, ErrSecurityPipelineNotReady
	}
	cand, permits, err := src.doorLane(ctx, c.Sel)
	if err != nil {
		return pass, err
	}
	pass.Sel = c.Sel
	pass.Sel.LaneID, pass.Sel.Provider = cand.lane.LaneName, cand.lane.ProviderName
	flag, err := admitLane(cand, pass.Tier, permits)
	if err != nil {
		pass.Flags = []string{flag}
		return pass, err
	}
	view := provider.ModelRequest{Sensitivity: pass.Tier, Policy: provider.Policy{ExternalAllowed: ClassifyLane(cand.provider) != LaneControllerLocal}}
	if m, ok := c.Payload.([]provider.ChatMessage); ok {
		view.Inputs = m
	}
	if err := e.pipeline.cfg.Policy.Authorize(ctx, view); err != nil {
		return pass, cascade.Wrap(cascade.KindPolicyDenied, err, "conductor: policy refused")
	}
	// The model names travel to the provider too (a request body field), so
	// they pass the same substitution as the payload.
	var model, selModel any
	if pass.Payload, err = e.substitutePayload(ctx, c.Payload, pass.Tier); err == nil {
		model, err = e.substitutePayload(ctx, c.Model, pass.Tier)
	}
	if err == nil {
		selModel, err = e.substitutePayload(ctx, pass.Sel.Model, pass.Tier)
	}
	if err != nil {
		pass.Flags = []string{"egress_substitution_failed"}
		return pass, ErrEgressSubstitutionFailed
	}
	pass.Model, pass.Sel.Model = model.(string), selModel.(string)
	return pass, nil
}

// leafTier resolves a leaf call's tier: the context tier (restricted when
// absent) joined with the thread privacy mode and the sensitivity gate, so
// no input can widen it.
func (e *Executor) leafTier(ctx context.Context) provider.SensitivityTier {
	declared, ok := DoorSensitivityFrom(ctx)
	if !ok {
		declared = provider.SensitivityRestricted
	}
	tiers := []provider.SensitivityTier{declared}
	if tp, ok := ThreadPrivacyFrom(ctx); ok {
		tiers = append(tiers, tp.EffectiveMode())
	}
	joined := provider.JoinSensitivity(tiers...)
	return provider.JoinSensitivity(joined, e.pipeline.cfg.Sensitivity.Resolve(joined))
}

// doorKey is the audit ParamsHash seed for a refusal.
func doorKey(c doorCall) string {
	if c.Req != nil {
		return c.Req.TaskID + "|" + c.Req.TaskClass
	}
	return c.Sel.LaneID + "|" + c.Sel.Model
}

// doorLane resolves sel against one registry snapshot. A named lane is read
// from the registry (sel.Provider must agree with it); an empty LaneID
// resolves by Model to exactly one lane, else KindInvalidInput.
func (r *DefaultRouter) doorLane(ctx context.Context, sel provider.Selection) (laneCandidate, RestrictedPermits, error) {
	snap, err := r.buildSnapshot(ctx)
	if err != nil {
		return laneCandidate{}, RestrictedPermits{}, err
	}
	var hits []laneCandidate
	for _, c := range snap.lanes {
		if (sel.LaneID != "" && c.lane.LaneName == sel.LaneID) || (sel.LaneID == "" && laneServes(c, sel.Model)) {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		// The caller's strings are never echoed: an error reaches the audit log.
		return laneCandidate{}, RestrictedPermits{}, cascade.Newf(cascade.KindInvalidInput,
			"conductor: selection resolves to %d lanes, want exactly one", len(hits))
	}
	if sel.Provider != "" && sel.Provider != hits[0].lane.ProviderName {
		return laneCandidate{}, RestrictedPermits{}, cascade.Newf(cascade.KindInvalidInput,
			"conductor: selection provider is not lane %q's provider", hits[0].lane.LaneName)
	}
	return hits[0], r.restrictedPermits, nil
}

// laneServes reports whether c serves model: its filter names it, or (no
// filter) its provider knows it.
func laneServes(c laneCandidate, model string) bool {
	if model == "" {
		return false
	}
	if len(c.lane.ModelFilter) > 0 {
		return slices.Contains(c.lane.ModelFilter, model)
	}
	return slices.Contains(c.provider.KnownModels, model)
}

// attemptFn is one dispatch to sel: the usage it consumed, whether any
// output already reached the caller, and its error.
type attemptFn func(ctx context.Context, sel provider.Selection) (provider.Usage, bool, error)

// withFailover runs run on sel and, on a capability-denied error before any
// output was delivered, re-selects EXACTLY ONCE with sel excluded and runs
// again (R-21.213). abandoned sees the superseded first attempt, so its usage
// is attributed, not lost. It returns the final Selection, attempt number,
// usage and error.
func (e *Executor) withFailover(ctx context.Context, req provider.ModelRequest, sel provider.Selection, run attemptFn, abandoned func(provider.Selection, provider.Usage, error)) (provider.Selection, int, provider.Usage, error) {
	u, delivered, err := run(ctx, sel)
	if err == nil || delivered || !cascade.HasKind(err, cascade.KindCapabilityDenied) {
		return sel, 1, u, err
	}
	reSel, selErr := e.router.Select(ctx, req, sel.LaneID)
	if selErr != nil {
		return sel, 1, u, err
	}
	abandoned(sel, u, err)
	u2, _, err2 := run(ctx, reSel)
	return reSel, 2, u2, err2
}

// doorChat, doorEmbed, doorCount and doorStream are the only calls into
// Pipeline's capability closures.
func (e *Executor) doorChat(ctx context.Context, sel provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error) {
	return e.pipeline.capability()(ctx, sel, req)
}

func (e *Executor) doorEmbed(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return e.pipeline.embedCapability()(ctx, sel, req)
}

func (e *Executor) doorCount(ctx context.Context, sel provider.Selection, req provider.CountRequest) (provider.CountResponse, error) {
	return e.pipeline.countCapability()(ctx, sel, req)
}

// doorStream dispatches a stream. delivered flips when the first event
// reaches sink, which is what makes a failover unsafe.
func (e *Executor) doorStream(ctx context.Context, sel provider.Selection, req provider.ChatRequest, delivered *atomic.Bool, sink provider.StreamSink) error {
	return e.pipeline.streamCapability()(ctx, sel, req, func(ev provider.StreamEvent) error {
		delivered.Store(true)
		return sink(ev)
	})
}

// doorPayload reads the substituted leaf payload back as T.
func doorPayload[T any](p doorPass) (T, error) {
	v, ok := p.Payload.(T)
	if !ok {
		return v, cascade.New(cascade.KindInternal, "conductor: door payload has an unexpected shape")
	}
	return v, nil
}

// finishLeaf writes the one terminal audit record of a dispatched leaf call
// and returns the dispatch error mapped onto the taxonomy.
func (e *Executor) finishLeaf(ctx context.Context, verb DoorVerb, pass doorPass, model string, err error) error {
	outcome, mapped, cause := "success", error(nil), error(nil)
	if err != nil {
		outcome, mapped = "provider_failure", mapProviderError(err)
		cause = mapped
		if ctx.Err() != nil {
			outcome, cause = "cancellation", context.Cause(ctx)
		}
	}
	e.auditDoor(ctx, verb, pass.Tier, pass.Sel.LaneID+"|"+model, pass.Sel, outcome, cause)
	return mapped
}
