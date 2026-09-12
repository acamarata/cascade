// Purpose: Driver, the local-model AgentProvider (AD/S-62.T1): an
//   in-process lane over the injected ModelExecutor seam (seams.go), with
//   Dispatch's R-21.170 per-model authoring gate on top of the plain
//   forwarding the first five AgentProvider verbs otherwise do.
// Inputs: a ModelExecutor, a *Qualifier, and a Clock, injected via New (no
//   global state, per this ticket's own task list).
// Outputs: provider.AgentProvider behavior.
// Constraints: Chat/Embed/Count/Stream/Capabilities forward unconditionally
//   (the shipped contract carries no task-class field for them — see
//   doc.go's contract-vs-tree note); Dispatch is where the authoring gate
//   lives. ProcessGroupID is always 0 (in-process, no child). No bare
//   time.Now; ids come from an injected counter, not math/rand.
//
// CONTRACT CORRECTION (FIX-manifest-collision-and-conductor-seam, not the
// original ticket's own scope): this file used to hold a raw
// provider.ModelProvider and call its Chat/Embed/Count/Stream methods
// directly, which internal/conductor's seam gate forbids (see seams.go's
// doc comment for the full reasoning and precedent this fix follows). This
// file now routes every call through the injected ModelExecutor instead.
// SPORT: providers.agents.local/ADD (P1-E30-W6-S62-T1); model-seam
//   CHANGE (raw ModelProvider -> ModelExecutor) —
//   FIX-manifest-collision-and-conductor-seam.

package local

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TaskClass mirrors the seven §5.16 task-class names this driver gates
// on. Declared locally: providers/** may import pkg/** only (Art.7.2),
// so this cannot reuse internal/conductor.TaskClass.
type TaskClass string

// The seven TaskClass members this ticket's gating rule names.
const (
	TaskClassClassify  TaskClass = "classify"
	TaskClassExtract   TaskClass = "extract"
	TaskClassSummarize TaskClass = "summarize"
	TaskClassCode      TaskClass = "code"
	TaskClassReason    TaskClass = "reason"
	TaskClassReview    TaskClass = "review"
	TaskClassArbitrate TaskClass = "arbitrate"
)

// authoringGated lists the task classes Dispatch refuses without a
// passing qualification row for the resolved model.
var authoringGated = map[TaskClass]bool{
	TaskClassCode: true, TaskClassReason: true,
	TaskClassReview: true, TaskClassArbitrate: true,
}

// ungated lists the task classes Dispatch always forwards.
var ungated = map[TaskClass]bool{
	TaskClassClassify: true, TaskClassExtract: true, TaskClassSummarize: true,
}

// ErrNotQualified is the domain sentinel Dispatch returns for a gated
// task class whenever the resolved model lacks `authoring`. It wraps the
// frozen KindCapabilityDenied per pkg/cascade's convention: a domain-
// specific sentinel wraps exactly one frozen Kind rather than the
// taxonomy growing a fifteenth member for it.
var ErrNotQualified = cascade.New(cascade.KindCapabilityDenied,
	"local: model is not qualified for authoring task classes")

// Driver is the local-model provider.AgentProvider: an in-process lane
// dispatching over model via the sel-based ModelExecutor leaf-dispatch
// seam (see this file's package doc comment). The zero value is not
// usable; construct with New.
type Driver struct {
	model ModelExecutor
	qual  *Qualifier

	mu      sync.Mutex
	jobs    map[provider.AgentJobID]*jobRecord
	nextID  uint64
	approve chan provider.ApprovalRequest
}

// New validates its seams and returns a ready Driver.
func New(model ModelExecutor, qual *Qualifier) (*Driver, error) {
	if model == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "local: driver model must not be nil")
	}
	if qual == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "local: driver qualifier must not be nil")
	}
	ch := make(chan provider.ApprovalRequest)
	close(ch) // the local lane never raises an approval request (R-21.171)
	return &Driver{
		model:   model,
		qual:    qual,
		jobs:    make(map[provider.AgentJobID]*jobRecord),
		approve: ch,
	}, nil
}

var _ provider.AgentProvider = (*Driver)(nil)

// Chat forwards unconditionally to the injected ModelExecutor, resolved by
// req.Model — the shipped AgentProvider.Chat carries no task-class field,
// so authoring gating does not apply here (see doc.go's contract-vs-tree
// note; use Dispatch for class-gated calls).
func (d *Driver) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	return d.model.Chat(ctx, modelSelection(req.Model), req)
}

// Embed forwards unconditionally to the injected ModelExecutor, resolved
// by req.Model.
func (d *Driver) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return d.model.Embed(ctx, modelSelection(req.Model), req)
}

// Count forwards unconditionally to the injected ModelExecutor, resolved
// by req.Model.
func (d *Driver) Count(ctx context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	return d.model.Count(ctx, modelSelection(req.Model), req)
}

// Stream forwards unconditionally to the injected ModelExecutor, resolved
// by req.Model.
func (d *Driver) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) error {
	return d.model.Stream(ctx, modelSelection(req.Model), req, sink)
}

// Capabilities returns the local lane's fixed CompliancePosture. lane is
// accepted for interface conformance and otherwise unused: a local
// instance has exactly one lane.
func (d *Driver) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return capabilities(), nil
}

// Dispatch is this package's task-class-gated entry point (see doc.go):
// classify/extract/summarize always forward to the injected ModelExecutor;
// code/reason/review/arbitrate forward only when req.Model (the resolved
// model id) carries a passing, current qualification row, else
// ErrNotQualified. Any other class is a client error, fail closed.
func (d *Driver) Dispatch(ctx context.Context, class TaskClass, req provider.ChatRequest) (provider.ChatResponse, error) {
	switch {
	case ungated[class]:
		return d.model.Chat(ctx, modelSelection(req.Model), req)
	case authoringGated[class]:
		qualified, err := d.qual.Resolve(ctx, req.Model)
		if err != nil {
			return provider.ChatResponse{}, err
		}
		if !qualified {
			return provider.ChatResponse{}, ErrNotQualified
		}
		return d.model.Chat(ctx, modelSelection(req.Model), req)
	default:
		return provider.ChatResponse{}, cascade.Newf(cascade.KindInvalidInput,
			"local: unrecognized task class %q", class)
	}
}

// AdvertisedCapabilities returns modelID's currently advertised
// capability set: classify/extract/summarize always, plus authoring only
// when Qualifier.Resolve reports a passing, current row for modelID.
func (d *Driver) AdvertisedCapabilities(ctx context.Context, modelID string) ([]Capability, error) {
	out := append([]Capability(nil), baseCapabilities...)
	qualified, err := d.qual.Resolve(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if qualified {
		out = append(out, CapabilityAuthoring)
	}
	return out, nil
}

// ApprovalRequests returns a closed, empty channel: the local lane never
// raises an approval request.
func (d *Driver) ApprovalRequests() <-chan provider.ApprovalRequest {
	return d.approve
}

// ResolveApproval always fails: no approval request is ever raised for
// this lane to resolve.
func (d *Driver) ResolveApproval(id string, _ string) error {
	return cascade.Newf(cascade.KindNotFound, "local: no such approval request %q", id)
}

// Spawn runs job.Prompt to completion synchronously, in-process, via the
// injected ModelExecutor, and stores the result under a freshly minted
// AgentJobID. ProcessGroupID is always 0: no child process is ever
// started (R-21.177's "0 only for an in-process lane" rule). Spawn
// refuses with ErrEntitlement when the lane's own declared
// CompliancePosture reports no programmatic entitlement, even though the
// fixed local posture always does today. AgentJobSpec carries no model id
// field, so the executor's own default resolution applies (modelSelection
// with an empty id), exactly as an unset ChatRequest.Model would.
func (d *Driver) Spawn(ctx context.Context, job provider.AgentJobSpec) (provider.SpawnResult, error) {
	if !capabilities().CompliancePosture.ProgrammaticEntitlement {
		return provider.SpawnResult{}, provider.ErrEntitlement
	}
	id := d.mintID()
	resp, err := d.model.Chat(ctx, modelSelection(""), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: job.Prompt}},
	})
	rec := &jobRecord{dataClass: job.DataClass.Resolved()}
	if err != nil {
		rec.state = provider.AgentRunFailed
		rec.output = err.Error()
	} else {
		rec.state = provider.AgentRunAccepted
		rec.output = resp.Message.Content
	}
	d.mu.Lock()
	d.jobs[id] = rec
	d.mu.Unlock()
	return provider.SpawnResult{JobID: id, ProcessGroupID: 0}, nil
}
