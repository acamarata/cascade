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
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no tier-file read RPC exists.
		{"cascade.read", "P1-E16-W4-S34-T5",
			"v2 has no RPC that reads a tier instruction file by tier name; the tier files are written by `cascade context harness sync`, not served"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no symbol-search entry point.
		{"cascade.search_codebase", "P1-E16-W4-S34-T5",
			"internal/repo builds a SymbolGraph for reachability only; it exposes no symbol-search entry point and no function-level index"},
		// CASCADE-ALLOW: P1-E37-W8-S73-T3 owns the compact-profile inbox tools.
		{"cascade.inbox.list", "P1-E37-W8-S73-T3",
			"cascade_inbox_list is a compact-profile name AK/S-73.T3 owns; internal/notify.Inbox is in-daemon memory with no RPC namespace"},
		// CASCADE-ALLOW: P1-E37-W8-S73-T3 owns cascade_cpa_send.
		{"cascade.inbox.send", "P1-E37-W8-S73-T3",
			"the mutating counterpart is cascade_cpa_send in the compact-write profile, which AK/S-73.T3 owns"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 concept did not survive v2.
		{"cascade.master_lists", "P1-E16-W4-S34-T5",
			"v2 has no project master-list surface; the concept did not survive the clean-sheet rewrite"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 different addressing model.
		{"cascade.memory.read", "P1-E16-W4-S34-T5",
			"v1 read a project memory FILE by (project, file); v2's store is record-addressed by (kind, name) and has no file-path surface"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 append is not write-a-record.
		{"cascade.memory.write", "P1-E16-W4-S34-T5",
			"v1 APPENDED to a project memory file; v2's memory.remember writes a whole record, a different operation needing its own schema decision"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no pbd.* RPC namespace.
		{"cascade.get_current", "P1-E16-W4-S34-T5", pbdReason},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no pbd.* RPC namespace.
		{"cascade.update_ticket_status", "P1-E16-W4-S34-T5", pbdReason},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no pbd.* RPC namespace.
		{"cascade.append_event", "P1-E16-W4-S34-T5", pbdReason},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no pbd.* RPC namespace.
		{"cascade.get_sprint", "P1-E16-W4-S34-T5", pbdReason},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no pbd.* RPC namespace.
		{"cascade.read_phase_status", "P1-E16-W4-S34-T5", pbdReason},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no pbd.* RPC namespace.
		{"cascade.list_tickets", "P1-E16-W4-S34-T5", pbdReason},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 needs an egress class first.
		{"cascade.check_routes", "P1-E16-W4-S34-T5",
			"v2 has no api-routes.yaml checker, and the tool issued outbound HTTP, which needs a registered egress class before a model may reach it"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 no filesystem inbox in v2.
		{"cascade.scan_inbox", "P1-E16-W4-S34-T5",
			"v1 drained a filesystem inbox directory; v2 routes notifications through internal/notify and has no such directory"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 detector is not a scanner.
		{"cascade.security.secret_scan", "P1-E16-W4-S34-T5",
			"internal/secrets ships a content Detector, not a repository scanner; there is no scan entry point to dispatch into"},
		// CASCADE-ALLOW: P1-E16-W4-S34-T5 audit runs in CI, not in process.
		{"cascade.security.audit", "P1-E16-W4-S34-T5",
			"v2 runs its dependency audit in CI's supply-chain lane, not through an in-process surface a tool could call"},
	}
}

// pbdReason is the one reason the six PBD phase-tree tools share, written
// once so six copies cannot drift into six slightly different claims.
const pbdReason = "the PBD phase tree belongs to plugins/pbd; internal/** must not import plugins/** " +
	"(the plugins-providers-import-pkg-only boundary runs in the other direction too, by design), " +
	"and no pbd.* RPC namespace is registered for this package to dispatch into"
