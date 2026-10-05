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
	// EventSessionStart reports a new harness session beginning. Captured
	// with a real fixture (testdata/cc-hook-fixtures/sessionstart.json).
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
	// EventSessionEnd reports a harness session ending. Captured with a
	// real fixture (testdata/cc-hook-fixtures/sessionend.json).
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
	// TimeoutSeconds is the per-hook timeout the harness enforces, in
	// seconds. Zero omits the field and leaves the harness's own default
	// in place — which is the right answer for a fire-and-forget POST
	// that already carries `curl -m 1`, and the wrong one for a hook the
	// harness WAITS on. P1-E16-W4-S34-T4's hydration hook is the second
	// kind: the harness blocks the user's prompt until it answers, so the
	// bound has to be stated rather than inherited.
	TimeoutSeconds int
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

// SessionsHookTimeoutSeconds is the harness-side bound on one sessions-pack
// hook, in the harness's own units. The hook command bounds its daemon call
// at one second itself; this is what stops a hook that hangs below that
// (a wedged syscall, a stalled disk) from holding the harness's hook chain.
const SessionsHookTimeoutSeconds = 5

// SessionsHookCommand is the command prefix every sessions-pack descriptor
// installs; the event name follows it. `cascade` by name, resolved through
// PATH at hook time, never a path into a source or build tree. The command
// reads the harness's native hook JSON on stdin, which is the only place
// the harness session id exists, and forwards it to the daemon; a fixed
// body (the curl template this replaced) could not carry it.
const SessionsHookCommand = "cascade fleet sessions hook-event"

// sessionsHookEvents is the closed, ordered set of events the sessions pack
// installs. Each is backed by a captured fixture
// (testdata/cc-hook-fixtures/<event>.json, provenance in testdata/README.md).
var sessionsHookEvents = []HookEventType{
	EventSessionStart, EventPreToolUse, EventPostToolUse, EventStop, EventSessionEnd,
}

// SessionsHookEvents returns the events the sessions pack installs, in
// install order. The slice is a copy; the hook-event command validates its
// event argument against it.
func SessionsHookEvents() []HookEventType {
	return append([]HookEventType(nil), sessionsHookEvents...)
}

// SessionsPack returns the "sessions" HookPack: one descriptor per event in
// SessionsHookEvents, each running `cascade fleet sessions hook-event
// <Event>`. Per the rule that no event type is supported without a
// captured fixture, SubagentStop, PreCompact and the rest are dispatch
// targets in handler.go but are not installed here.
//
// The command is non-gating: it exits 0 on every path and never 2, so a
// sessions hook can never block a tool call.
func SessionsPack() HookPack {
	descriptors := make([]HookDescriptor, 0, len(sessionsHookEvents))
	for _, evt := range sessionsHookEvents {
		descriptors = append(descriptors, HookDescriptor{
			EventType:       evt,
			Matcher:         "",
			CommandTemplate: SessionsHookCommand + " " + string(evt),
			TimeoutSeconds:  SessionsHookTimeoutSeconds,
		})
	}
	return HookPack{Name: "sessions", Descriptors: descriptors}
}
