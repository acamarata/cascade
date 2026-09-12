// Package local is the local-model AgentProvider driver (AD/S-62.T1): an
// in-process lane that dispatches over the J/S-19.T5 ModelProvider seam
// via model.execute — no subprocess, no new egress class. It implements
// pkg/provider.AgentProvider so a locally-served model (typically an
// Ollama instance, reached through the injected provider.ModelProvider)
// can sit in the same lane pool a plugin or vendor-hosted driver occupies.
//
// AUTHORING IS A PER-MODEL CAPABILITY, NOT A FLAG (R-21.170). The
// classify/extract/summarize task classes always forward to the injected
// ModelProvider. The code/reason/review/arbitrate task classes forward
// only when the resolved model id (ChatRequest.Model) carries a passing,
// current qualification row — see qualification.go. There is no
// constructor option, operator override, or environment variable that
// grants authoring any other way: Dispatch and AdvertisedCapabilities are
// the only two exported functions that read the resolved answer, and
// Qualifier.Run is the only one that can ever write it.
//
// CONTRACT-VS-TREE NOTE (see this ticket's journal for the full record):
// the shipped pkg/provider.AgentProvider.Chat/Stream/Spawn surface carries
// no task-class field anywhere in its request shapes (ChatRequest,
// AgentJobSpec) — a caller that needs class-based authoring gating must
// use this package's own Dispatch(ctx, class, req), which threads
// ChatRequest.Model through as the resolved model id. Chat/Embed/
// Count/Stream forward unconditionally, exactly as an api-backed
// ModelProvider driver's five verbs would, matching the AgentProvider
// doc's own description of the first five methods as "the exact same
// shape as ModelProvider". The AI/S-71.T3 hard denylist that structurally
// blocks a learned proposal from ever flipping a capability does not
// exist in this tree yet (Epic AI is Wave 7, later than this Wave 6
// ticket): this package instead exposes no mutator authoring could be
// flipped through in the first place — Qualifier.Run is the only write
// path, gated on a real fixture pass, and there is no setter of any kind
// on Qualifier or Driver. TestLocalAuthoringOnHardDenylist asserts this
// structural property; wiring providers/agents/local's capability names
// into the S-71.T3 denylist file itself is deferred to that ticket,
// which is the first point such a file exists to add them to.
//
// Inputs: a provider.ModelProvider (the J/S-19.T5 seam), a ConfigStore
// (the `config` storage-domain seam), a Clock, and the named qualification
// fixture's raw bytes.
// Outputs: provider.AgentProvider behavior; QualificationRow records.
// Constraints: providers/** imports pkg/** only (Art.7.2) — no
// internal/storage, no internal/conductor. No bare time.Now (Clock is
// injected). No os.Stdout/Stderr. No net import.
//
// SPORT: providers.agents.local/ADD (P1-E30-W6-S62-T1).
package local
