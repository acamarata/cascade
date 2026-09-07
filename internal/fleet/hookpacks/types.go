// Package hookpacks defines the harness hook pack emitter layer: the
// descriptor types a hook pack renders into an installable hook
// configuration, and the wire payload shape a rendered hook's command
// posts back to the daemon over fleet.sessions.hook_event.
//
// Purpose: give any harness a small, generic vocabulary of hook events
//
//	(HookEventType), a way to describe one installable hook
//	(HookDescriptor), a named bundle of them (HookPack), and the JSON-RPC
//	2.0 params a rendered hook's command sends back (HookPayload).
//
// Inputs: none (this file is pure type/constant declarations).
// Outputs: HookEventType, HookDescriptor, HookPack, HookPayload.
// Constraints: HookPayload is a closed allowlist by construction — it has
//
//	no field for transcript text, prompts, tool output, or any other
//	value a harness's hook JSON might carry. handler.go decodes the wire
//	payload with unknown fields rejected, so a field this struct never
//	declared cannot reach any code path in this package, let alone
//	storage: redaction here is "the struct has no slot for it," not a
//	deny-list scan run over a raw blob.
//
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks

// HookEventType names one hook lifecycle event a harness can report.
// The set is closed and matches, one for one, every row the R-16.48
// mapping table (handler.go) dispatches on — a value outside this set
// never reaches a dispatch case, it is refused by ParseHookEventType
// instead (fail-closed, never a silent new member).
type HookEventType string

const (
	// EventSessionStart reports a new harness session beginning.
	EventSessionStart HookEventType = "SessionStart"
	// EventInstructionsLoaded reports the harness finished loading its
	// project/session instructions.
	EventInstructionsLoaded HookEventType = "InstructionsLoaded"
	// EventUserPromptSubmit reports the user submitted a prompt.
	EventUserPromptSubmit HookEventType = "UserPromptSubmit"
	// EventPreToolUse reports a tool call about to run. Captured with a
	// real fixture (testdata/cc-hook-fixtures/pretooluse.json).
	EventPreToolUse HookEventType = "PreToolUse"
	// EventPostToolUse reports a tool call finishing. Captured with a
	// real fixture (testdata/cc-hook-fixtures/posttooluse.json).
	EventPostToolUse HookEventType = "PostToolUse"
	// EventPostToolBatch reports a batch of tool calls finishing.
	EventPostToolBatch HookEventType = "PostToolBatch"
	// EventStop reports the session going idle. Captured with a real
	// fixture (testdata/cc-hook-fixtures/stop.json).
	EventStop HookEventType = "Stop"
	// EventSubagentStart reports a spawned child session beginning.
	EventSubagentStart HookEventType = "SubagentStart"
	// EventSubagentStop reports a spawned child session ending.
	EventSubagentStop HookEventType = "SubagentStop"
	// EventTaskCreated reports a tracked task being created.
	EventTaskCreated HookEventType = "TaskCreated"
	// EventTaskCompleted reports a tracked task completing.
	EventTaskCompleted HookEventType = "TaskCompleted"
	// EventWorktreeCreate reports a worktree being created.
	EventWorktreeCreate HookEventType = "WorktreeCreate"
	// EventWorktreeRemove reports a worktree being removed.
	EventWorktreeRemove HookEventType = "WorktreeRemove"
	// EventPreCompact reports a context compaction about to run.
	EventPreCompact HookEventType = "PreCompact"
	// EventPostCompact reports a context compaction finishing.
	EventPostCompact HookEventType = "PostCompact"
	// EventSessionEnd reports a harness session ending.
	EventSessionEnd HookEventType = "SessionEnd"
)

// knownEventTypes is ParseHookEventType's fail-closed lookup table: the
// single source of truth for valid names, mirroring
// internal/fleet/sessions.sessionStateNames's pattern for the identical
// reason — a typo or a future harness's unrecognized event name must be
// refused, never silently accepted as a new member.
var knownEventTypes = map[HookEventType]bool{
	EventSessionStart:       true,
	EventInstructionsLoaded: true,
	EventUserPromptSubmit:   true,
	EventPreToolUse:         true,
	EventPostToolUse:        true,
	EventPostToolBatch:      true,
	EventStop:               true,
	EventSubagentStart:      true,
	EventSubagentStop:       true,
	EventTaskCreated:        true,
	EventTaskCompleted:      true,
	EventWorktreeCreate:     true,
	EventWorktreeRemove:     true,
	EventPreCompact:         true,
	EventPostCompact:        true,
	EventSessionEnd:         true,
}

// ParseHookEventType reports whether name is a known HookEventType.
func ParseHookEventType(name HookEventType) bool {
	return knownEventTypes[name]
}

// HookDescriptor describes one installable hook: which event fires it,
// an optional matcher (e.g. a tool-name pattern; empty matches every
// occurrence of EventType), and the shell command template that reports
// the event to the daemon. CommandTemplate carries the literal
// socketPlaceholder token (renderer.go) in place of the resolved daemon
// socket path — it is never a hard-coded path.
type HookDescriptor struct {
	EventType       HookEventType
	Matcher         string
	CommandTemplate string
}

// HookPack is a named, ordered set of HookDescriptors that install and
// render together. RegisterPack (registry.go) keys packs by Name.
type HookPack struct {
	Name        string
	Descriptors []HookDescriptor
}

// HookPayload is the fleet.sessions.hook_event JSON-RPC 2.0 method's
// params shape: exactly the six fields a rendered hook's command reports,
// and nothing else. handler.go's decoder rejects any additional field
// outright (DisallowUnknownFields) rather than accepting and dropping
// it — the allowlist is this struct's field list itself.
type HookPayload struct {
	// Harness identifies which harness reported the event (e.g. a
	// config-registered harness name). Never validated against a fixed
	// literal set here: harnesses are configured, not hard-coded.
	Harness string `json:"harness"`
	// EventType is the HookEventType the harness reported.
	EventType HookEventType `json:"event_type"`
	// SessionID is the harness's own session identifier.
	SessionID string `json:"session_id"`
	// PID is the harness process's OS process id.
	PID int `json:"pid"`
	// Account is the caller-supplied account label, if any.
	Account string `json:"account"`
	// TimestampMs is the event instant, unix milliseconds.
	TimestampMs int64 `json:"timestamp_ms"`
}

// sessionsPackCommand builds one event's command template: a curl call
// against the daemon's unix socket with a short timeout, always
// exiting 0 (`|| true`) so a slow, absent, or wedged daemon never blocks
// or fails the harness's own hook chain — the property this ticket's
// non-negotiables call out as the one that matters most. Actual
// harness-supplied field substitution (session id, pid, account) is
// P/S-34.T1's install-step concern (full_desc: "this ticket delivers
// the DATA STRUCTURES... only"); this template's params object carries
// only the literal, always-known event_type so it is runnable and
// testable as it stands.
func sessionsPackCommand(evt HookEventType) string {
	return `curl -s -m 1 --unix-socket ` + socketPlaceholder +
		` -X POST http://cascade.sock/rpc -H "Content-Type: application/json"` +
		` -d '{"jsonrpc":"2.0","id":1,"method":"` + string(MethodHookEvent) +
		`","params":{"event_type":"` + string(evt) + `"}}' >/dev/null 2>&1 || true`
}

// SessionsPack returns this ticket's own "sessions" HookPack: one
// descriptor per event type backed by a real captured fixture
// (testdata/cc-hook-fixtures/*.json) — PreToolUse, PostToolUse, and
// Stop. Per HOW step 1/6, "No event type is supported without a
// captured fixture": SubagentStop and PreCompact are handler.go dispatch
// targets (the R-16.48 mapping table must handle them regardless) but
// are not installed by this pack, since no fixture for either was
// captured this run (testdata/README.md).
func SessionsPack() HookPack {
	return HookPack{
		Name: "sessions",
		Descriptors: []HookDescriptor{
			{EventType: EventPreToolUse, Matcher: "", CommandTemplate: sessionsPackCommand(EventPreToolUse)},
			{EventType: EventPostToolUse, Matcher: "", CommandTemplate: sessionsPackCommand(EventPostToolUse)},
			{EventType: EventStop, Matcher: "", CommandTemplate: sessionsPackCommand(EventStop)},
		},
	}
}
