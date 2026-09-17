package coretools

// Purpose: the v1 MCP tools this build does NOT register, each with the
//
//	ticket that owns the missing surface and the reason it is missing
//	(Art.1.3 allowlist discipline; R-14.251 rule 5).
//
// Inputs: none.
// Outputs: the deferral list the v1-parity golden test joins against.
// Constraints: this list may only SHRINK. A tool moves from here to
//
//	specs.go when a real v2 surface answers it; nothing is added here to
//	make a failing parity test pass, because the test compares this list
//	against the harvested v1 inventory and a v1 tool that is in neither
//	list fails it.
//
// SPORT: internal/mcp/coretools (ADD) — P1-E16-W4-S34-T2.

// Ticket values that are not tickets.
//
// A deferral's Ticket normally names the open ticket that owns the missing
// surface. Two outcomes are not that, and encoding them as a ticket-shaped
// string would be a lie the parity test could not catch (R-14.266):
const (
	// Retired means the CONCEPT does not return. The Reason says so in as
	// many words and names the ruling that decided it.
	Retired = "retired"
	// ServedByPlugin means the concept survives and a plugin serves it.
	// coretools is the wrong layer: internal/** may not import plugins/**,
	// and the plugin's own manifest already surfaces its tools. The Reason
	// names the surface a caller should use instead.
	ServedByPlugin = "served-by-plugin"
)

// Deferral is one v1 tool this build does not expose.
type Deferral struct {
	// V1Name is the dotted v1 tool name.
	V1Name string
	// Ticket is the open ticket that owns the missing surface.
	Ticket string
	// Reason says what is actually missing. "Not implemented" is not a
	// reason; each entry below names the specific thing v2 does not have.
	Reason string
}

// Deferrals returns every v1 tool this build does not register.
//
// Seventeen of v1's twenty-four tools are here, and the shape of the gap
// is worth stating plainly rather than burying in a count: v1's MCP
// surface was largely a filesystem API over a project's .claude tree —
// tier files, master lists, a PCI inbox directory, a PBD phase tree of
// YAML. v2 does not have those things. The seven tools that DO carry over
// are the ones whose concept survived the clean-sheet rewrite: retrieval,
// context assembly, harness sync, and the memory record store.
func Deferrals() []Deferral {
	return []Deferral{
		// CASCADE-ALLOW: P1-E33-W7-S67-T3 registers the symbol graph as a corpus.
		{"cascade.search_codebase", "P1-E33-W7-S67-T3",
			"recall.query already searches this tree textually and is registered; a SYMBOL-level index is a " +
				"different capability that internal/repo's reachability-only graph does not yet provide. " +
				"AG/S-67.T3 builds it and registers it as a `graph` corpus in the retrieval engine — at " +
				"which point recall.query answers this without a second tool (R-14.266)"},
		// CASCADE-ALLOW: P1-E37-W8-S73-T3 owns the compact-profile inbox tools.
		{"cascade.inbox.list", "P1-E37-W8-S73-T3",
			"cascade_inbox_list is a compact-profile name AK/S-73.T3 owns; internal/notify.Inbox is in-daemon memory with no RPC namespace"},
		// CASCADE-ALLOW: P1-E37-W8-S73-T3 owns cascade_cpa_send.
		{"cascade.inbox.send", "P1-E37-W8-S73-T3",
			"the mutating counterpart is cascade_cpa_send in the compact-write profile, which AK/S-73.T3 owns"},
		// CASCADE-ALLOW: Retired by R-14.266.
		{"cascade.master_lists", Retired,
			"RETIRED by R-14.266. v1's master lists were a file in a project's .claude tree; v2 has no such " +
				"tree and no master-list concept. Nothing replaces it and nothing is planned to."},
		// CASCADE-ALLOW: Retired by R-14.266.
		{"cascade.memory.read", Retired,
			"RETIRED by R-14.266. v1 read a project memory FILE by (project, file). v2's store is " +
				"record-addressed, and cascade_memory_recall and cascade_memory_search — both registered — " +
				"answer the question a model asks. The file-path addressing does not return."},
		// CASCADE-ALLOW: Retired by R-14.266.
		{"cascade.memory.write", Retired,
			"RETIRED by R-14.266. v1 APPENDED to a project memory file. v2 writes whole records through " +
				"cascade_memory_remember, which is registered. There is deliberately no append: a record " +
				"store with an append operation has two ways to hold one fact, and they drift."},
		// CASCADE-ALLOW: Served by cascade-pbd's own tools (R-14.266).
		{"cascade.get_current", ServedByPlugin, pbdStatusReason},
		// CASCADE-ALLOW: Served by cascade-pbd's own RPC (R-14.266).
		{"cascade.update_ticket_status", ServedByPlugin, pbdLifecycleReason},
		// CASCADE-ALLOW: Served by cascade-pbd's own RPC (R-14.266).
		{"cascade.append_event", ServedByPlugin, pbdLifecycleReason},
		// CASCADE-ALLOW: Served by cascade-pbd's own tools (R-14.266).
		{"cascade.get_sprint", ServedByPlugin, pbdBoardReason},
		// CASCADE-ALLOW: Served by cascade-pbd's own tools (R-14.266).
		{"cascade.read_phase_status", ServedByPlugin, pbdStatusReason},
		// CASCADE-ALLOW: Served by cascade-pbd's own tools (R-14.266).
		{"cascade.list_tickets", ServedByPlugin, pbdBoardReason},
		// CASCADE-ALLOW: Retired by R-14.266.
		{"cascade.check_routes", Retired,
			"RETIRED by R-14.266. v2 has no api-routes.yaml to check, and the tool issued outbound HTTP — " +
				"a model-callable surface that makes arbitrary requests needs a registered egress class, " +
				"which nothing would register for a concept this build does not have."},
		// CASCADE-ALLOW: P1-E37-W8-S73-T3 owns the compact-profile inbox tools.
		{"cascade.scan_inbox", "P1-E37-W8-S73-T3",
			"v1 drained a filesystem inbox directory; v2 routes notifications through internal/notify, and " +
				"the read surface a model gets is cascade_inbox_list, which AK/S-73.T3 owns alongside the " +
				"other two inbox tools (R-14.266)"},
		// CASCADE-ALLOW: Retired by R-14.266.
		{"cascade.security.secret_scan", Retired,
			"RETIRED by R-14.266, and the reason is not that it would be hard. A tool that scans a " +
				"repository for credentials and returns what it found to a model is an exfiltration " +
				"surface with a helpful name: the model's transcript leaves the machine. internal/secrets' " +
				"Detector stays where it is, guarding what cascade itself sends."},
		// CASCADE-ALLOW: Retired by R-14.266.
		{"cascade.security.audit", Retired,
			"RETIRED by R-14.266. The dependency audit runs in CI's supply-chain lane, against the whole " +
				"module graph, on every push. A model asking one in-process question would get a narrower " +
				"answer from a build that has already been audited."},
	}
}

// The three reasons the six PBD phase-tree tools share, written once each
// so six copies cannot drift into six slightly different claims.
//
// The earlier single reason said "no pbd.* RPC namespace is registered for
// this package to dispatch into". Half of that was right and half was not:
// the boundary is real — internal/** may not import plugins/** — but the
// namespace exists. cascade-pbd registers plugin.pbd.status, .board,
// .claim, .step and .done, and surfaces the two read-only ones as MCP
// tools through its own manifest. So these concepts survive; coretools is
// simply the wrong layer to serve them from (R-14.266).
const (
	pbdStatusReason = "SERVED by cascade-pbd as cascade_plugin_pbd_status (plugin.pbd.status). " +
		"coretools cannot register it: internal/** may not import plugins/**, and the plugin's own " +
		"manifest already surfaces it."
	pbdBoardReason = "SERVED by cascade-pbd as cascade_plugin_pbd_board (plugin.pbd.board), which lists " +
		"one phase's ticket tree grouped by model class. coretools cannot register it: internal/** may " +
		"not import plugins/**."
	pbdLifecycleReason = "SERVED by cascade-pbd's plugin.pbd.claim/step/done RPC, which records a " +
		"ticket's lifecycle transitions. Deliberately NOT an MCP tool: the plugin's manifest exposes " +
		"only its two read-only surfaces, so a model can read the board and not move a ticket on it."
)
