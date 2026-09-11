package provider

// Purpose: AgentProvider (R-14.50, AD/S-61.T1) — the contract a plugin
//
//	satisfying agent dispatch must implement, feeding N/S-29 and R/S-40
//	model routing. Its first five methods are the exact same shape as
//	ModelProvider (model.go/types.go): chat, embed, count, stream,
//	capabilities — because from the router's perspective a plugin-hosted
//	agent and an api-backed model driver are interchangeable dispatch
//	targets. compliance.go's CompliancePosture applies to AgentProvider
//	exactly as it does to ModelProvider (R-16.10): both publish it through
//	their Capabilities descriptor.
//
//	AD/S-61.T1 EXTENDS this interface in place (R-16.68a) with the job
//	dispatch verbs a driver under providers/agents/<name> implements:
//	Spawn/ApprovalRequests/ResolveApproval/Message/Status/Cancel/
//	Collect/Artifacts, plus the R-21.158 protocol-pin verbs
//	SupportedProtocols/Negotiate. This ticket creates no pkg/agent
//	package (02-TARGET-STRUCTURE fixes pkg to {cascade, provider,
//	plugin}) and adds no internal/ type to the interface.
//
// Inputs: none at this layer — a contract, not behavior.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); no
//
//	duplicate types — reuses ChatRequest/ChatResponse/ModelEmbedRequest/
//	ModelEmbedResponse/CountRequest/CountResponse/StreamSink/Capabilities
//	from model.go and types.go, and CompliancePosture from compliance.go,
//	rather than declaring parallel ones. internal/jobs maps the DTOs on
//	this interface (AgentJobSpec, AgentJobID, AgentRunState, AgentEvent,
//	CollectResult, ApprovalRequest) to job records at its own boundary;
//	Status returns the pure AgentRunState, never a jobs.JobState.
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1).

import "context"

// AgentProvider is the contract a plugin or providers/agents/<name>
// driver satisfying agent dispatch must implement.
type AgentProvider interface {
	// Chat completes req as a single, non-streaming exchange.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// Embed returns one vector per req.Inputs entry, in order.
	Embed(ctx context.Context, req ModelEmbedRequest) (ModelEmbedResponse, error)
	// Count returns the plugin's own token count for req.Text.
	Count(ctx context.Context, req CountRequest) (CountResponse, error)
	// Stream completes req as a sequence of typed events delivered to
	// sink, in order, terminating in exactly one done or error event.
	Stream(ctx context.Context, req ChatRequest, sink StreamSink) error
	// Capabilities describes the named lane's current tool-capability
	// support and compliance posture, exactly as ModelProvider.Capabilities
	// does for an api-backed driver.
	Capabilities(ctx context.Context, lane string) (Capabilities, error)

	// Spawn starts a new background agent job from job. Every driver that
	// spawns a child process spawns it in ITS OWN process group and
	// returns that pgid on SpawnResult (0 only for an in-process lane);
	// internal/jobs records the pgid on the execution row at spawn.
	// Spawn returns ErrEntitlement when the lane's CompliancePosture
	// reports no programmatic entitlement, and ErrPreSpawnSecretFound
	// (fail-closed, no child started) when PreSpawnScan hits.
	Spawn(ctx context.Context, job AgentJobSpec) (SpawnResult, error)
	// ApprovalRequests streams every ApprovalRequest this provider raises
	// across all its jobs. A driver that answers its own approval instead
	// of enqueueing one here fails the conformance suite (R-21.171):
	// approvals are enqueued and the run blocks.
	ApprovalRequests() <-chan ApprovalRequest
	// ResolveApproval resolves the ApprovalRequest named id using token.
	ResolveApproval(id string, token string) error
	// Message delivers turn as the next input to the running job id.
	Message(ctx context.Context, id AgentJobID, turn string) error
	// Status returns id's current AgentRunState. It returns
	// ErrJobNotFound for an unknown id.
	Status(ctx context.Context, id AgentJobID) (AgentRunState, error)
	// Cancel runs the R-21.174/R-21.140 cancel ladder: SIGTERM to id's
	// process group, wait DriverDefaults.CancelGrace, SIGKILL the process
	// group, wait for confirmed exit, and only then report
	// AgentRunCancelled. Until exit is confirmed the job's Status reads
	// AgentRunCancelling and no contender is admitted to its lease. Exit
	// unconfirmed past the deadline returns ErrCancelUnconfirmed and
	// leaves the job AgentRunCancelling. Cancel returns ErrJobNotFound
	// for an unknown id.
	Cancel(ctx context.Context, id AgentJobID) error
	// Collect blocks until id reaches a terminal AgentRunState and
	// returns its CollectResult. It returns ErrJobNotFound for an unknown
	// id, including one never spawned.
	Collect(ctx context.Context, id AgentJobID) (CollectResult, error)
	// Artifacts lists id's discovered artifact paths. It returns a
	// non-nil, possibly empty, slice on success, and ErrJobNotFound for
	// an unknown id.
	Artifacts(ctx context.Context, id AgentJobID) ([]string, error)

	// SupportedProtocols returns this driver's declared ProtocolRange.
	SupportedProtocols() ProtocolRange
	// Negotiate pins ONE ProtocolVersion for the life of a job from the
	// intersection of this driver's range and peer, at startup, before
	// the first turn. An empty intersection returns
	// ErrHarnessIncompatible — the one uniform RUNTIME refusal
	// (R-21.158), never a build-time-only check.
	Negotiate(ctx context.Context, peer ProtocolRange) (ProtocolVersion, error)
}
