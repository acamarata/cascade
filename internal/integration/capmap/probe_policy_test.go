//go:build capmap

package capmap

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func init() {
	register("TestCapmap_AuditLogApprovalDeny", probeAuditApprovalDeny)
	register("TestCapmap_McpServerServe", probeMCPServe)
}

// auditRecord is the part of a policy audit record the probes read.
type auditRecord struct {
	Seq      int64  `json:"seq"`
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Hash     string `json:"hash"`
	PrevHash string `json:"prev_hash"`
}

// auditRecords reads the audit log through the CLI.
func (c *cli) auditRecords() []auditRecord {
	c.t.Helper()
	e := decodeEnvelope(c.t, c.mustOK("", "policy", "audit", "query", "--json"))
	var d struct {
		Records []auditRecord `json:"records"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil || !e.OK {
		c.t.Fatalf("audit query: ok=%v err=%v", e.OK, err)
	}
	return d.Records
}

// pendingApprovals returns the ids of the queued approval requests.
func (c *cli) pendingApprovals() []string {
	c.t.Helper()
	e := decodeEnvelope(c.t, c.mustOK("", "approval", "list", "--json"))
	var d struct {
		Pending []struct {
			ID string `json:"request_id"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil || !e.OK {
		c.t.Fatalf("approval list: ok=%v err=%v", e.OK, err)
	}
	ids := make([]string, 0, len(d.Pending))
	for _, p := range d.Pending {
		ids = append(ids, p.ID)
	}
	return ids
}

// requireChain fails unless the records are one gapless hash chain.
func requireChain(t *testing.T, recs []auditRecord) {
	t.Helper()
	for i, r := range recs {
		if r.Hash == "" || r.Seq != int64(i+1) {
			t.Fatalf("record %d = %+v, want seq %d and a hash", i, r, i+1)
		}
		if i > 0 && r.PrevHash != recs[i-1].Hash {
			t.Fatalf("record %d prev_hash %q does not match record %d hash %q", i, r.PrevHash, i-1, recs[i-1].Hash)
		}
	}
}

// probeAuditApprovalDeny proves a queued approval can be decided and that
// the decision is chained into the durable audit log. Authorization: a
// browser-shaped approval.deny is refused and the request stays queued.
// Routing: the owner's `approval deny` removes it from the queue the
// engine filled. Side effect: an approval.deny record is appended to a
// gapless hash chain and survives a daemon restart. Result: the CLI
// reports the request denied.
func probeAuditApprovalDeny(t *testing.T) {
	c := newCLI(t)
	sock := c.startDaemon().Daemon.SocketPath
	ids := c.pendingApprovals()
	if len(ids) == 0 {
		c.mustOK("", "mcp", "tools", "list")
		ids = c.pendingApprovals()
	}
	if len(ids) == 0 {
		t.Fatal("no approval request is queued, so the decision path cannot be exercised")
	}
	id := ids[0]
	mustRefuseBrowser(t, sock, "approval.deny", map[string]string{"request_id": id})
	if !contains(c.pendingApprovals(), id) {
		t.Fatal("a refused approval.deny removed the request from the queue")
	}
	if out := c.mustOK("", "approval", "deny", id).stdout; !strings.Contains(out, "denied") {
		t.Fatalf("approval deny stdout = %q, want a denied receipt", out)
	}
	if contains(c.pendingApprovals(), id) {
		t.Fatal("the request is still queued after deny")
	}
	assertDenyRecorded := func() {
		recs := c.auditRecords()
		requireChain(t, recs)
		for _, r := range recs {
			if r.Kind == "approval.deny" {
				return
			}
		}
		t.Fatalf("no approval.deny record in %+v", recs)
	}
	assertDenyRecorded()
	c.stopDaemon()
	c.startDaemon()
	assertDenyRecorded()
}

// contains reports whether s holds v.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// mcpLine is one JSON-RPC response from `cascade mcp serve`.
type mcpLine struct {
	ID     int `json:"id"`
	Result struct {
		IsError bool `json:"isError"`
		Tools   []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// mcpSession feeds requests to `mcp serve` on stdin and returns the
// responses by id.
func (c *cli) mcpSession(reqs ...string) map[int]mcpLine {
	c.t.Helper()
	head := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"capmap","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	r := c.mustOK(head+strings.Join(reqs, "\n")+"\n", "mcp", "serve")
	out := map[int]mcpLine{}
	for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var l mcpLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			c.t.Fatalf("mcp serve wrote a non-JSON line %q: %v", line, err)
		}
		out[l.ID] = l
	}
	return out
}

// probeMCPServe proves the MCP server is policy-filtered and routes a
// write tool to the daemon. Authorization: tools/list omits the memory
// write tool and calling it is a typed error that stores nothing.
// Routing and side effect: cascade_cpa_send is served by the daemon's
// journal, so chat.get_thread returns the turn text. Result: the call
// returns a thread id and a turn id.
func probeMCPServe(t *testing.T) {
	const text = "mcp probe turn"
	c := newCLI(t)
	sock := c.startDaemon().Daemon.SocketPath
	call := func(id int, name, args string) string {
		return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	}
	got := c.mcpSession(
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "cascade_cpa_send", `{"content":"`+text+`"}`),
		call(4, "cascade_memory_remember", `{"content":"must not store"}`),
	)
	names := map[string]bool{}
	for _, tool := range got[2].Result.Tools {
		names[tool.Name] = true
	}
	if !names["cascade_cpa_send"] || names["cascade_memory_remember"] {
		t.Fatalf("tools/list = %v, want cascade_cpa_send and no cascade_memory_remember", names)
	}
	if got[4].Error == nil || len(got[4].Result.Content) != 0 {
		t.Fatalf("withheld tool call = %+v, want a typed error", got[4])
	}
	if strings.Contains(c.mustOK("", "memory", "list").stdout, "must not store") {
		t.Fatal("the withheld memory tool stored a record")
	}
	send := got[3]
	if send.Error != nil || send.Result.IsError || len(send.Result.Content) != 1 {
		t.Fatalf("cascade_cpa_send = %+v, want a result", send)
	}
	var receipt struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
	}
	if err := json.Unmarshal([]byte(send.Result.Content[0].Text), &receipt); err != nil || receipt.ThreadID == "" || receipt.TurnID == "" {
		t.Fatalf("receipt %q: %v, want thread_id and turn_id", send.Result.Content[0].Text, err)
	}
	thread := mustRPC(t, sock, "chat.get_thread", map[string]string{"thread_id": receipt.ThreadID})
	if !strings.Contains(string(thread), text) || !strings.Contains(string(thread), receipt.TurnID) {
		t.Fatalf("daemon thread = %s, want turn %s with %q", thread, receipt.TurnID, text)
	}
}
