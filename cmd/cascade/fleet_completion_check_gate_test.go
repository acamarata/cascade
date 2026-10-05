//go:build !windows

// Purpose: the reply-decoding and fail-closed proofs for the completion-gate
//
//	hook command, run as the harness runs it: the rendered command under
//	/bin/sh, the real cascade binary, a raw unix-socket responder for replies
//	a real daemon never sends (reordered or spaced JSON, JSON without an
//	explicit deny, non-JSON, an oversized body, a late reply) and a live
//	daemon for the rows that need real daemon behaviour. Every row of the
//	fail-closed table must exit 2 with a stderr reason; one control row proves
//	the same rig can exit 0.
//
// SPORT: cmd/cascade/fleet-completion-check (coverage for P1-CI-09).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// replyID is the id member every well-formed raw reply carries.
const replyID = `"id":"` + completionRPCID + `"`

func TestCompletionCheckReorderedJSON(t *testing.T) {
	bin := buildCascadeBinary(t)
	spaced := "{ \"jsonrpc\" : \"2.0\",\n  " + replyID + ",\n  \"result\" : {\n    \"deny\" : %s\n  }\n}\n"
	cases := []struct {
		name, body string
		wantExit   int
		wantReason string
	}{
		{"allow_reordered", `{"result":{"reason":"","deny":false},` + replyID + `,"jsonrpc":"2.0"}`, 0, ""},
		{"allow_spaced", strings.Replace(spaced, "%s", "false", 1), 0, ""},
		{"allow_extra_members", `{"server_version":"x","result":{"note":"\"deny\":true","extra":[1,{"deny":true}],"deny":false},` + replyID + `,"jsonrpc":"2.0"}`, 0, ""},
		{"deny_reordered", `{"result":{"reason":"missing evidence","deny":true},` + replyID + `,"jsonrpc":"2.0"}`, 2, "missing evidence"},
		{"deny_spaced", strings.Replace(spaced, "%s", "true", 1), 2, completionCheckDeniedFallback},
		{"deny_beside_allow_lookalike", `{"result":{"deny":true,"reason":"blocked"},"note":"\"deny\":false",` + replyID + `}`, 2, "blocked"},
		{"deny_reason_holds_allow_lookalike", `{"result":{"reason":"say \"deny\":false","deny":true},` + replyID + `}`, 2, `say "deny":false`},
		{"deny_multiline_reason", `{"result":{"deny":true,"reason":"first\nsecond\r\nthird"},` + replyID + `}`, 2, "first second"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := startRawResponder(t, rawHTTP(200, tc.body), 0)
			res := runShell(t, renderedCompletionCommand(t, bin, sock, hookpacks.EventStop), nil, "", 10*time.Second)
			if res.code != tc.wantExit {
				t.Fatalf("exit = %d (stderr=%q), want %d", res.code, res.stderr, tc.wantExit)
			}
			if tc.wantExit == 0 {
				if res.stderr != "" {
					t.Fatalf("an allow wrote to stderr: %q", res.stderr)
				}
				return
			}
			if !strings.Contains(res.stderr, tc.wantReason) {
				t.Fatalf("stderr = %q, want it to carry the reason %q", res.stderr, tc.wantReason)
			}
			if lines := strings.Count(strings.TrimRight(res.stderr, "\n"), "\n") + 1; lines != 1 {
				t.Fatalf("stderr has %d lines, want one: %q", lines, res.stderr)
			}
		})
	}
}

// failClosedRow is one fail-closed scenario: how to build the socket (and an
// optional command or environment override) and how long it may take.
type failClosedRow struct {
	name   string
	socket func(t *testing.T) string
	env    []string
	cmd    func(bin, socket string) string
	bound  time.Duration
	reason string
}

func rawRow(name, body string) failClosedRow {
	return failClosedRow{name: name, bound: 10 * time.Second, socket: func(t *testing.T) string {
		return startRawResponder(t, rawHTTP(200, body), 0)
	}}
}

// rawReplyBodies maps a row name to a reply body that must never allow.
func rawReplyBodies() map[string]string {
	return map[string]string{
		"non_json_body": `not json at all`, "empty_body": ``, "json_array": `[]`, "json_scalar": `true`,
		"no_result_member":   `{"jsonrpc":"2.0",` + replyID + `}`,
		"result_empty":       envelope(`{}`),
		"result_no_deny":     envelope(`{"reason":"r"}`),
		"result_null":        envelope(`null`),
		"deny_null":          envelope(`{"deny":null}`),
		"deny_string_false":  envelope(`{"deny":"false"}`),
		"deny_number_zero":   envelope(`{"deny":0}`),
		"reason_wrong_type":  envelope(`{"deny":false,"reason":7}`),
		"rpc_error":          `{"jsonrpc":"2.0",` + replyID + `,"error":{"code":-32603,"message":"boom"}}`,
		"rpc_error_and_deny": `{"jsonrpc":"2.0",` + replyID + `,"error":{"code":-32603,"message":"boom"},"result":{"deny":false}}`,
		"wrong_id":           `{"jsonrpc":"2.0","id":"someone-else","result":{"deny":false}}`,
		// Truncated bodies: the prefix reads like an allow but is not JSON.
		"truncated_in_result":   `{"jsonrpc":"2.0",` + replyID + `,"result":{"deny":false`,
		"truncated_after_value": `{"jsonrpc":"2.0",` + replyID + `,"result":{"deny":false}`,
		"truncated_mid_key":     `{"jsonrpc":"2.0",` + replyID + `,"result":{"den`,
		"trailing_garbage":      envelope(`{"deny":false}`) + `}{`,
	}
}

func failClosedRows() []failClosedRow {
	rows := []failClosedRow{
		{name: "socket_missing", bound: 10 * time.Second, socket: func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "none.sock")
		}},
		{name: "daemon_stopped", bound: 10 * time.Second, socket: func(t *testing.T) string {
			sock, stop := startCompletionDaemon(t, nil)
			stop()
			return sock
		}},
		{name: "deadline_exceeded", bound: 30 * time.Second, reason: "timeout", socket: func(t *testing.T) string {
			return startRawResponder(t, rawHTTP(200, envelope(`{"deny":false}`)), 16*time.Second)
		}},
		{name: "real_gate_unknown_job", bound: 20 * time.Second, reason: "unknown job id", env: []string{"CASCADE_JOB_ID=ghost-job"},
			socket: func(t *testing.T) string { s, _ := startCompletionDaemon(t, nil); return s }},
		{name: "oversized_reply", bound: 20 * time.Second, socket: func(t *testing.T) string {
			return startRawResponder(t, rawHTTP(200, envelope(`{"deny":false}`)+strings.Repeat(" ", 5<<20)), 0)
		}},
		{name: "unknown_binary_path", bound: 10 * time.Second, socket: func(t *testing.T) string {
			return startRawResponder(t, rawHTTP(200, envelope(`{"deny":false}`)), 0)
		}, cmd: func(_, sock string) string {
			return `'/nonexistent/dir/cascade' fleet completion-check --event Stop --socket '` + sock + `' || exit 2`
		}},
		{name: "unknown_event", bound: 10 * time.Second, reason: "--event", socket: func(t *testing.T) string {
			return startRawResponder(t, rawHTTP(200, envelope(`{"deny":false}`)), 0)
		}, cmd: func(bin, sock string) string {
			return `'` + bin + `' fleet completion-check --event Bogus --socket '` + sock + `' || exit 2`
		}},
	}
	bodies := rawReplyBodies()
	for name, body := range bodies {
		rows = append(rows, rawRow(name, body))
	}
	// A connection that closes before Content-Length bytes arrive.
	short := rawHTTP(200, envelope(`{"deny":false}`))
	rows = append(rows, failClosedRow{name: "connection_cut_mid_body", bound: 10 * time.Second,
		socket: func(t *testing.T) string { return startRawResponder(t, short[:len(short)-12], 0) }})
	return rows
}

func TestCompletionCheckFailsClosed(t *testing.T) {
	bin := buildCascadeBinary(t)
	t.Run("control_allow_exits_zero", func(t *testing.T) {
		sock := startRawResponder(t, rawHTTP(200, envelope(`{"deny":false}`)), 0)
		res := runShell(t, renderedCompletionCommand(t, bin, sock, hookpacks.EventStop), nil, "", 10*time.Second)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("control: exit=%d stderr=%q, want a silent 0", res.code, res.stderr)
		}
	})
	t.Run("binary_without_exec_bit", func(t *testing.T) {
		noExec := filepath.Join(t.TempDir(), "cascade")
		if err := os.WriteFile(noExec, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sock := startRawResponder(t, rawHTTP(200, envelope(`{"deny":false}`)), 0)
		res := runShell(t, renderedCompletionCommand(t, noExec, sock, hookpacks.EventStop), nil, "", 10*time.Second)
		if res.code != 2 || res.stderr == "" {
			t.Fatalf("exit=%d stderr=%q, want 2 with a reason", res.code, res.stderr)
		}
	})
	for _, row := range failClosedRows() {
		t.Run(row.name, func(t *testing.T) {
			sock := row.socket(t)
			command := renderedCompletionCommand(t, bin, sock, hookpacks.EventStop)
			if row.cmd != nil {
				command = row.cmd(bin, sock)
			}
			res := runShell(t, command, row.env, "", row.bound)
			if res.code != 2 {
				t.Fatalf("exit = %d (stdout=%q stderr=%q), want 2", res.code, res.stdout, res.stderr)
			}
			if strings.TrimSpace(res.stderr) == "" {
				t.Fatal("a fail-closed exit carried no stderr reason")
			}
			if row.reason != "" && !strings.Contains(res.stderr, row.reason) {
				t.Fatalf("stderr = %q, want it to mention %q", res.stderr, row.reason)
			}
		})
	}
}
