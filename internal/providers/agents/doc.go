// Package agents binds each agent harness to the plugin that owns it and to
// its driver factory, projects a verified scope envelope onto the driver
// spawn spec, and is the only production importer of providers/agents/<driver>.
//
// Purpose: the harness binding table (harness id -> owning plugin -> driver
// factory -> closed credential table), its read-only listing, the Resolver
// the host shim (P1-AGT-03) uses to turn a lane into a factory, and
// ProjectEnvelope.
// Inputs: Binding rows registered at init by harness_<id>.go; a lane such as
// "agent:claude"; a signed envelope and the pinned controller key.
// Outputs: Binding copies, DriverFactory values and provider.SpawnEnvelope.
// Constraints: no spawn happens here and nothing here fills a SpawnEnvelope
// except ProjectEnvelope; a harness whose owning plugin is absent or disabled
// is unavailable, never replaced by another harness; the local model-door
// lane is never registered as a harness (ADR-0002 section 1); credential
// variable names come only from a Binding's closed table, never from
// operator configuration. internal/build's arch gates confine driver imports
// and Spawn calls to this package and the shim.
// SPORT: internal.providers.agents/ADD (P1-AGT-02, contract:harness-agent-binding).
package agents
