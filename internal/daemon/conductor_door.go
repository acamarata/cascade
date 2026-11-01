package daemon

// Purpose: the one in-process conductor door (contract:conductor-door-
//   inprocess, P1-CPA-10). Embeddings, the classifier and reply calls inside
//   the daemon reach the model layer through ConductorDoor, never through a
//   socket method: no conductor.embed verb exists, so no client can declare
//   an embedding tier.
// Inputs: the *conductor.Executor RegisterConductorExecuteHandler builds,
//   the provider registry reader it routed over, its audit writer and its
//   construction error, all handed over once through the unexported bind.
// Outputs: Embed, Execute and ExecuteStream, each riding the Executor's
//   enforced door (P1-SEC-01) and its live deny and quota (P1-CAP-07).
// Constraints: one door per process; only this package binds it. Embed's
//   tier is an explicit argument the caller computes from daemon-side
//   state; the door never defaults it upward and refuses every tier but
//   internal and public before any dispatch. The caller's ctx reaches the
//   Executor unchanged apart from the door tier.
// SPORT: internal/daemon (ADD, P1-CPA-10).

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// errConductorDoorNotBound is every door method's answer before
// RegisterConductorExecuteHandler has bound the process door.
var errConductorDoorNotBound = cascade.New(cascade.KindUnavailable, "conductor door not bound")

// ConductorDoor is the daemon's in-process entry to the conductor. Callers
// get it from ProcessConductorDoor; RegisterConductorExecuteHandler binds
// it to the Executor it builds. The door satisfies provider.ModelExecutor.
type ConductorDoor struct {
	mu     sync.RWMutex
	exec   *conductor.Executor
	reader provider.ProviderRegistryReader
	audit  audit.Writer
	cerr   error
}

var _ provider.ModelExecutor = (*ConductorDoor)(nil)

// processDoor is the one door per process.
var processDoor = &ConductorDoor{}

// ProcessConductorDoor returns this process's door: the same pointer on
// every call, never nil. It answers KindUnavailable until bound.
func ProcessConductorDoor() *ConductorDoor {
	return processDoor
}

// bind points the door at the Executor a daemon composition built (or at
// its construction failure). A later composition in the same process
// replaces the earlier binding. RegisterConductorExecuteHandler is the one
// caller (internal/build TestArchOneConductorExecuteRegistration).
func (d *ConductorDoor) bind(exec *conductor.Executor, reader provider.ProviderRegistryReader, auditWriter audit.Writer, cerr error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exec, d.reader, d.audit, d.cerr = exec, reader, auditWriter, cerr
}

// state returns the bound collaborators, or the reason the door cannot
// dispatch.
func (d *ConductorDoor) state() (*conductor.Executor, provider.ProviderRegistryReader, audit.Writer, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.cerr != nil {
		return nil, nil, nil, cascade.Wrap(cascade.KindUnavailable, d.cerr, "conductor door: executor unavailable")
	}
	if d.exec == nil || d.reader == nil || d.audit == nil {
		return nil, nil, nil, errConductorDoorNotBound
	}
	return d.exec, d.reader, d.audit, nil
}

// Ready reports nil only when the door is bound to a constructed Executor.
func (d *ConductorDoor) Ready() error {
	_, _, _, err := d.state()
	return err
}

// Embed embeds inputs on the named lane at tier. A tier other than internal
// or public is refused KindPolicyDenied, with one audit record, before any
// lane lookup or dispatch. The lane resolves by exact LaneName from the
// registry, else KindNotFound. The dispatch passes the Executor's model.embed
// door with tier in ctx, so the door's own tier check sees it.
func (d *ConductorDoor) Embed(ctx context.Context, lane, model string, tier provider.SensitivityTier, inputs []string) ([][]float32, error) {
	exec, reader, aw, err := d.state()
	if err != nil {
		return nil, err
	}
	if tier != provider.SensitivityInternal && tier != provider.SensitivityPublic {
		cause := cascade.Newf(cascade.KindPolicyDenied,
			"conductor door: embedding refused at tier %s: only internal and public content may be embedded", tier)
		auditDoorRefusal(ctx, aw, tier, lane, model, cause)
		return nil, cause
	}
	sel, err := resolveEmbedLane(ctx, reader, lane, model)
	if err != nil {
		return nil, err
	}
	resp, err := exec.Embed(conductor.ContextWithDoorSensitivity(ctx, tier), sel,
		provider.ModelEmbedRequest{Inputs: inputs, Model: model})
	if err != nil {
		return nil, err
	}
	return resp.Vectors, nil
}

// resolveEmbedLane maps a lane name onto the Selection Executor.Embed
// admits. Only an exact LaneName match counts: no alias, no prefix, no
// fallback to another lane.
func resolveEmbedLane(ctx context.Context, reader provider.ProviderRegistryReader, lane, model string) (provider.Selection, error) {
	lanes, err := reader.ListLanes(ctx)
	if err != nil {
		return provider.Selection{}, cascade.Wrap(cascade.KindUnavailable, err, "conductor door: list lanes")
	}
	for _, l := range lanes {
		if lane != "" && l.LaneName == lane {
			return provider.Selection{LaneID: l.LaneName, Provider: l.ProviderName, Model: model}, nil
		}
	}
	return provider.Selection{}, cascade.Newf(cascade.KindNotFound, "conductor door: no lane named %q", lane)
}

// Execute runs req through the Executor's model.execute door: authorize
// (classifier, tier, owner deny), Router.Select (quota), then dispatch. The
// caller's ctx passes unchanged, principal and cancellation included.
func (d *ConductorDoor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	exec, _, _, err := d.state()
	if err != nil {
		return provider.ModelResponse{}, err
	}
	return exec.Execute(ctx, req)
}

// ExecuteStream runs req through the Executor's model.execute_stream door,
// on the caller's ctx unchanged. The returned func cancels the stream. It
// enters through ExecuteStreamJob, the production streaming entry, so the
// job's cancel registration and terminal event match every other stream.
func (d *ConductorDoor) ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, func(), error) {
	exec, _, _, err := d.state()
	if err != nil {
		return nil, nil, err
	}
	_, ch, cancel, err := exec.ExecuteStreamJob(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return ch, cancel, nil
}

// auditDoorRefusal writes the one record of a tier refusal. The record
// names the lane and model by hash only, never the inputs.
func auditDoorRefusal(ctx context.Context, aw audit.Writer, tier provider.SensitivityTier, lane, model string, cause error) {
	_, _ = aw.Append(context.WithoutCancel(ctx), audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor.door",
		Action:     string(conductor.VerbEmbed),
		ParamsHash: audit.HashParams([]byte(lane + "|" + model)),
		RiskLevel:  tier.String(),
		Verdict:    "refusal",
		Outcome:    "refusal",
		Explain:    refusalExplain(cause),
	})
}

// refusalExplain is the record's JSON rationale: the outcome and the reason.
func refusalExplain(cause error) json.RawMessage {
	explain, _ := json.Marshal(struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}{Outcome: "refusal", Reason: cause.Error()})
	return explain
}
