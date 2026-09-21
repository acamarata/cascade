// Package coretools declares the first-party MCP tool set — the tools a
// harness sees when it talks to `cascade mcp serve`, as opposed to the
// tools a loaded plugin contributes.
//
// Purpose: the v1-parity MCP tool set (P1-E16-W4-S34-T2, R-14.251): one
//
//	Spec per v1 tool a v2 RPC method really answers, naming the method,
//	the capability a caller must hold, and the JSON Schema of the
//	arguments that method actually decodes.
//
// Inputs: none — Specs is a declaration, not a computation.
// Outputs: the ordered spec set. deferrals.go carries the v1 tools no v2
//
//	surface answers, and testdata/v1-goldens/tools.json pins both halves
//	against the harvested v1 inventory.
//
// Constraints: a Spec's Schema describes the params of ITS OWN method,
//
//	harvested from that method's Go params type — never v1's schema, which
//	describes arguments a v2 handler does not read. The v1 schema is
//	recorded in the golden as evidence of what v1 promised; the two are
//	reconciled by a human, not by a machine. Names follow 07's
//	cascade_<noun>_<verb> mirror rule (R-21.179), never v1's dotted form.
//
// SPORT: internal/mcp/coretools (ADD) — P1-E16-W4-S34-T2.
package coretools

// PluginID is the owner every first-party tool in this package reports.
// It is the core, not a plugin: these tools are served by the binary
// cascade-claude registers, not by anything loaded into it.
const PluginID = "cascade.core.tools"

// Capability names, as registered in the policy capability registry.
// They are constants because the registry seeds them and this package
// asks for them by the same name; a literal typed twice is a capability
// that half-exists.
const (
	// CapabilityContextRead covers every read of the local knowledge
	// base: retrieval, context assembly, and harness context.
	CapabilityContextRead = "context.read"
	// CapabilityMemoryRead covers reads of the memory record store.
	CapabilityMemoryRead = "memory.read"
	// CapabilityMemoryWrite covers writes and retirements in that store.
	CapabilityMemoryWrite = "memory.write"
	// CapabilityPluginsRead covers reads of the plugin registry catalog
	// (P1-E24-W5-S50-T2).
	CapabilityPluginsRead = "plugins.read"
)

// Spec is one first-party MCP tool.
type Spec struct {
	// Name is the MCP tool name, per 07's cascade_<noun>_<verb> rule.
	Name string
	// Alias is the v1 tool name callers written against v1 may still
	// use, bound to the SAME handler so the two cannot answer
	// differently. Empty for a tool with no v1 alias.
	Alias string
	// V1Name is the dotted v1 name this tool descends from, recorded so
	// the golden's parity check has something to join on.
	V1Name string
	// Description is what the model reads to decide whether to call it.
	Description string
	// Method is the JSON-RPC method that answers this tool. It is the
	// SAME method the daemon registers, dispatched through the same
	// registry — this package adds no second implementation of anything.
	Method string
	// Capability is the policy capability a caller must hold for this
	// tool to appear in tools/list.
	Capability string
	// Mutating reports whether calling this tool changes state. It is
	// read by the profile tickets (AK/S-73.T3, AP/S-82.T1): no mutating
	// tool is eligible for the READ-ONLY compact profile (R-21.179).
	Mutating bool
	// Schema is the tool's JSON Schema, describing the params Method
	// really decodes.
	Schema map[string]any
}

// object builds a JSON Schema object node. It exists so every schema
// below is written the same way and so `additionalProperties: false` is
// not something a spec can forget: a tool that silently accepts unknown
// arguments teaches a model that the arguments it invented were fine.
func object(required []string, properties map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"required":             required,
		"additionalProperties": false,
		"properties":           properties,
	}
}

// str, num and boolean build the leaf nodes.
func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumStr(description string, values ...string) map[string]any {
	vals := make([]any, len(values))
	for i, v := range values {
		vals[i] = v
	}
	return map[string]any{"type": "string", "description": description, "enum": vals}
}

func num(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolean(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// Specs returns the registered v1-parity tool set, in a stable order.
//
// Every entry's Schema is transcribed from its method's own params type,
// cited in the comment above it. A method whose params change and whose
// schema does not is a drift this package cannot detect for you — which
// is why each citation names the exact Go type to re-read.
func Specs() []Spec {
	out := retrievalSpecs()
	out = append(out, contextSpecs()...)
	out = append(out, memorySpecs()...)
	return append(out, pluginSpecs()...)
}

// retrievalSpecs are the tools over the retrieval engine. Split from
// contextSpecs because the two answer different questions — "find me a
// passage" and "what am I operating under" — and because one function
// carrying every knowledge-base tool outgrew the 50-line cap the moment
// R-14.266 registered the tier reader.
func retrievalSpecs() []Spec {
	return []Spec{
		// recall.QueryParams (internal/retrieval/recall/rpc.go).
		{
			Name:   "cascade_context_search",
			Alias:  "cascade_search",
			V1Name: "cascade.search",
			Description: "Search the local knowledge base. Fuses full-text and dense retrieval and " +
				"returns ranked passages with citations naming the file each came from.",
			Method:     "recall.query",
			Capability: CapabilityContextRead,
			Schema: object([]string{"query", "scope"}, map[string]any{
				"query":       str("Natural-language search text."),
				"scope":       str("The asking session's scope reference."),
				"corpus":      map[string]any{"type": "array", "description": "Named corpora to search. Empty searches every corpus this scope authorizes.", "items": map[string]any{"type": "string"}},
				"entitlement": str("Highest privacy tier this query may see. Empty resolves to the project tier."),
				"k":           num("Maximum results. Zero uses the server default."),
				"cite":        boolean("Also return the rendered Markdown citation block."),
			}),
		},
	}
}

// contextSpecs are the tools over context assembly and harness sync: what
// the session is operating under, and keeping it current.
func contextSpecs() []Spec {
	return []Spec{
		// daemon.ContextShowParams (internal/daemon/context_assemble.go).
		//
		// Registered by P1-E16-W4-S34-T5, which found the deferral's
		// premise false: context.show has been on the daemon since
		// E/S-09.T2 and returns every resolved tier by NAME with its full
		// content, which is exactly what v1's cascade.read answered. The
		// deferral said "v2 has no RPC that reads a tier instruction file
		// by tier name" — nobody had looked (R-14.266).
		//
		// v1 took a tier and returned one file; this takes a working
		// directory and returns every tier. That is the better shape for
		// the question a model actually asks ("what instructions am I
		// under?") and it is the shape that already exists, so no new
		// surface was forged to narrow it.
		{
			Name:   "cascade_context_show",
			V1Name: "cascade.read",
			Description: "Show every resolved context tier for a working directory, in order, with " +
				"each tier's full instruction text and token count. This is what the session is " +
				"operating under.",
			Method:     "context.show",
			Capability: CapabilityContextRead,
			Schema: object([]string{"cwd"}, map[string]any{
				"cwd": str("Absolute path of the working directory whose tiers to show."),
			}),
		},
		// daemon.ContextAssembleParams (internal/daemon/context_assemble.go).
		{
			Name:   "cascade_context_slice",
			V1Name: "cascade.context_slice",
			Description: "Assemble a token-budgeted context slice for the given working directory, " +
				"deduplicated and windowed, ready to paste into a prompt.",
			Method:     "context.slice",
			Capability: CapabilityContextRead,
			Schema: object([]string{"cwd"}, map[string]any{
				"cwd":        str("Absolute path of the working directory to assemble context for."),
				"max_tokens": num("Token budget for the assembled slice. Omit for the configured default."),
			}),
		},
		// daemon.ContextSyncParams (internal/daemon/context_sync.go).
		{
			Name:   "cascade_context_sync",
			V1Name: "cascade.provide_harness_context",
			Description: "Bring this working directory's harness instruction files up to date and " +
				"report what changed. Use check_only to see the drift without writing anything.",
			Method:     "context.sync",
			Capability: CapabilityContextRead,
			Mutating:   true,
			Schema: object([]string{"cwd"}, map[string]any{
				"cwd":        str("Absolute path of the working directory to sync."),
				"check_only": boolean("Report drift without writing any file."),
			}),
		},
	}
}

// memorySpecs are the tools over the memory record store.
func memorySpecs() []Spec {
	return []Spec{
		// memory.RememberParams (internal/memory/rpc.go).
		{
			Name:   "cascade_memory_remember",
			V1Name: "cascade.memory.remember",
			Description: "Write a record into the memory store and return its canonical address. " +
				"Use this for a fact worth recalling in a later session, not for this session's working notes.",
			Method:     "memory.remember",
			Capability: CapabilityMemoryWrite,
			Mutating:   true,
			Schema: object([]string{"content"}, map[string]any{
				"content":    str("The record body."),
				"type":       enumStr("Record kind. Defaults to project.", "user", "feedback", "project", "reference"),
				"name":       str("Record name within its kind. Empty derives a name from the body."),
				"provenance": str("Reference to the session that produced this record."),
			}),
		},
		// memory.RecallParams (internal/memory/rpc_query.go).
		{
			Name:        "cascade_memory_recall",
			V1Name:      "cascade.memory.recall",
			Description: "Find records in the memory store whose name, description or body match a query.",
			Method:      "memory.recall",
			Capability:  CapabilityMemoryRead,
			Schema:      recallSchema(),
		},
		// memory.RecallParams again: v1's own description said this tool
		// is "Identical to recall", so it is bound to the same method
		// rather than given a second, differently-behaving search path.
		{
			Name:        "cascade_memory_search",
			V1Name:      "cascade.memory.search",
			Description: "Search the memory store. Identical to cascade_memory_recall; kept for v1 callers.",
			Method:      "memory.recall",
			Capability:  CapabilityMemoryRead,
			Schema:      recallSchema(),
		},
		// memory.ForgetParams (internal/memory/rpc_forget.go).
		{
			Name:   "cascade_memory_forget",
			V1Name: "cascade.memory.forget",
			Description: "Retire one memory record by its canonical address. " +
				"Pass dry_run to see what retiring it would do without doing it.",
			Method:     "memory.forget",
			Capability: CapabilityMemoryWrite,
			Mutating:   true,
			Schema: object([]string{"id"}, map[string]any{
				"id":      str(`Canonical "<kind>/<name>" address to retire.`),
				"reason":  str("Why this record is being retired. Never required."),
				"dry_run": boolean("Report what would be retired without retiring it."),
			}),
		},
	}
}

// recallSchema is memory.recall's params, shared by the two tools bound
// to that method so they cannot describe the same arguments differently.
func recallSchema() map[string]any {
	return object([]string{"query"}, map[string]any{
		"query": str("Substring to match, case-insensitively, against a record's name, description and body."),
		"k":     num("Maximum results. Zero returns zero — a caller asking for none is answered with none."),
		"type":  enumStr("Narrow the scan to one record kind. Empty scans all four.", "user", "feedback", "project", "reference"),
	})
}
