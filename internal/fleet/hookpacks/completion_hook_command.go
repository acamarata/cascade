package hookpacks

// Purpose (this file): builds the completion-gate hook's own command
//
//	template (completionHookCommand) -- the one command the harness executes
//	for TaskCompleted/Stop. The command is a single invocation of the
//	cascade binary's hidden `fleet completion-check` subcommand
//	(cmd/cascade/fleet_completion_check.go), which reads the harness JSON on
//	stdin, builds the completion.check params with encoding/json from the
//	environment, makes one RPC call over the daemon socket and decodes the
//	reply into a typed struct. The earlier template did all of that in POSIX
//	sh: it matched the reply by substring and spliced $CASCADE_SESSION_ID,
//	$CASCADE_JOB_ID and $CASCADE_TICKET_ID into JSON unescaped (AUD-035).
//	sh has no JSON decoder and jq is not guaranteed on a harness host, so the
//	body moved into the binary.
//
// Inputs: a HookEventType.
//
// Outputs: one POSIX sh command string still carrying the unresolved
//
//	binaryPlaceholder and socketPlaceholder tokens (renderer.go substitutes
//	both, single-quoted).
//
// Constraints: CC's own hook contract has NO daemon-side fallback --
//
//	whatever this string does IS the fail-open/fail-closed behavior in the
//	field. The subcommand exits 0 only for a decoded `deny: false` and exits
//	2 with a one-line reason for everything else; the trailing `|| exit 2`
//	turns any other way the process can end (a crash, a signal, a missing or
//	non-executable binary) into the same exit 2, so no outcome but an
//	explicit allow lets the harness proceed. The command is straight-line:
//	exactly one invocation, nothing re-issues it, so a Stop -> block -> Stop
//	replay can never become more than one request per invocation
//	(TestCompletionHookCommand_RendersExactlyOneCall), and stop_hook_active
//	changes no exit-code branch
//	(TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed). The
//	cascade binary is named by absolute path, never resolved through PATH
//	(TestCompletionHookCommandUsesAbsolutePath).
//
// SPORT: fleet/hookpacks.completionHookCommand/ADD (P1-E32-W6-S66-T1),
// CHANGE (P1-CI-09).

import "time"

// completionHookClientSlack is added to the server-side completion_timeout
// to form the subcommand's own request deadline (R-16.74's "server + 5s"):
// strictly greater than handleCompletionHook's own deadline, so the client
// can never give up before a genuine server-side Deny would have won the
// race.
const completionHookClientSlack = 5 * time.Second

// CompletionClientBudget is the deadline `cascade fleet completion-check`
// puts on its one daemon round trip: the daemon's completion timeout plus
// completionHookClientSlack.
func CompletionClientBudget() time.Duration {
	return defaultCompletionTimeout + completionHookClientSlack
}

// completionHookCommand builds one event's synchronous command template.
// serverTimeout is accepted for the registration call site's sake but not
// rendered: the subcommand derives its deadline from CompletionClientBudget,
// which is built on the same defaultCompletionTimeout every registration
// passes today (no [fleet.hooks] config section exists to pass another, the
// Art.9 gap completion_gate.go already names).
func completionHookCommand(evt HookEventType, _ time.Duration) string {
	return binaryPlaceholder + ` fleet completion-check --event ` + string(evt) +
		` --socket '` + socketPlaceholder + `' || exit 2`
}
