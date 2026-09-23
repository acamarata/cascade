package hookpacks

// Purpose (this file): builds the completion-gate hook's own shell command
//
//	template (completionHookCommand) -- the literal POSIX sh curl
//	invocation the harness actually executes for TaskCompleted/Stop. Split
//	out of completion_gate.go to keep that file's own R-16.16 dispatch
//	logic under the 300-line cap while this file carries the CR-B/D1
//	shell-layer fix on its own: CC's own hook contract has NO daemon-side
//	fallback -- whatever this string does IS the fail-open/fail-closed
//	behavior in the field, so every branch is proven directly against
//	/bin/sh in completion_hook_command_test.go, not inferred from the
//	Go-side handleCompletionHook this template merely calls into.
//
// Inputs: a HookEventType and the server-side completion_timeout that
//
//	governs handleCompletionHook's own context.WithTimeout (defaultCompletionTimeout,
//	completion_gate.go -- no [fleet.hooks] config section reads a
//	different value yet, a disclosed Art.9 gap that file already names).
//
// Outputs: one POSIX sh command string, still carrying the unresolved
//
//	socketPlaceholder token (renderer.go substitutes it).
//
// Constraints: fail CLOSED (exit 2, one-line reason on stderr) on every
//
//	outcome except a well-formed 2xx JSON body containing the literal
//	`"deny":false` -- curl's own non-zero exit, an unreachable socket, a
//	non-2xx HTTP status, an empty body and unparseable JSON all take the
//	SAME exit-2 branch a real `"deny":true"` does (CR-B Q1: the PREVIOUS
//	version of this template exited 0 -- allow -- on every one of those,
//	because it only ever matched the deny:true case and fell through to
//	an unconditional `exit 0`). The client-side `-m` budget is always
//	serverTimeout+completionHookClientSlack: strictly greater than
//	handleCompletionHook's own deadline, so curl can never give up and
//	fall through before a genuine server-side Deny would have won the
//	race (R-16.74's own text: "server + 5s").
//
// SPORT: fleet/hookpacks.completionHookCommand/ADD (P1-E32-W6-S66-T1).

import (
	"math"
	"strconv"
	"time"
)

// completionHookClientSlack is added to the server-side completion_timeout
// to form curl's own -m budget (R-16.74).
const completionHookClientSlack = 5 * time.Second

// completionHookCommand builds one event's synchronous command template: a
// curl call that WAITS for the daemon's response (unlike
// sessionsPackCommand's fire-and-forget `|| true`) and translates every
// non-explicit-allow outcome into the harness's own blocking contract (a
// non-zero exit with a one-line reason on stderr). $CASCADE_JOB_ID/
// $CASCADE_TICKET_ID/$CASCADE_SESSION_ID are the real environment a
// Cascade-dispatched driver inherits (pkg/provider.driverEnvAllowlistBase);
// an ordinary human session simply leaves them unset, the unscoped case
// completion_scope.go documents.
//
// It also reads the harness's own native hook JSON off stdin (real
// capture: testdata/completion/stop_fixture.json is compact, no
// whitespace) once, to pull out stop_hook_active -- the harness's replay
// signal after a prior block -- and forwards it verbatim as
// CompletionHookPayload.StopHookActive. This is a STRAIGHT-LINE script:
// exactly one curl call, no branch re-issues it, so a harness-level
// Stop -> block -> Stop replay can never become more than one request per
// invocation (TestCompletionHookCommand_RendersExactlyOneCurlCall), and
// the flag changes no exit-code branch below (fail-closed either way,
// TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed).
func completionHookCommand(evt HookEventType, serverTimeout time.Duration) string {
	clientSeconds := int(math.Ceil((serverTimeout + completionHookClientSlack).Seconds()))
	return `stdin_json=$(cat); active=false; case "$stdin_json" in *'"stop_hook_active":true'*) active=true;; esac; ` +
		`resp=$(curl -s -m ` + strconv.Itoa(clientSeconds) + ` -w '\n%{http_code}' --unix-socket ` + socketPlaceholder +
		` -X POST http://cascade.sock/rpc -H "Content-Type: application/json"` +
		` -d '{"jsonrpc":"2.0","id":1,"method":"` + MethodCompletionCheck +
		`","params":{"event_type":"` + string(evt) + `","session_id":"'"$CASCADE_SESSION_ID"'",` +
		`"job_id":"'"$CASCADE_JOB_ID"'","task_id":"'"$CASCADE_TICKET_ID"'","stop_hook_active":'"$active"'}}' 2>/dev/null); rc=$?; ` +
		`if [ "$rc" -ne 0 ]; then echo "completion check request failed (curl exit $rc)" >&2; exit 2; fi; ` +
		`code=$(printf '%s' "$resp" | tail -n 1); body=$(printf '%s' "$resp" | sed '$d'); ` +
		`if [ "$code" != "200" ]; then echo "completion check request failed (http $code)" >&2; exit 2; fi; ` +
		`case "$body" in *'"deny":false'*) exit 0;; *'"deny":true'*) echo "$body" | sed -n 's/.*"reason":"\([^"]*\)".*/\1/p' >&2; exit 2;; *) echo "completion check response malformed" >&2; exit 2;; esac`
}
