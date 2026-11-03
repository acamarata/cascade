package codex

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// approvalKit is a kit whose session raises one command approval (rpc id
// 100) before turn/completed.
func approvalKit(t *testing.T) *kit {
	k := newKit(t)
	k.useSession(beforeTurnCompleted(loadSession(t), approvalLine(350, "100", cmdApproval, cmdParams)))
	return k
}

// noAnswer fails when any frame answers rpc id among written.
func noAnswer(written []map[string]json.RawMessage, id string) bool {
	for _, m := range written {
		if _, isMethod := m["method"]; !isMethod && string(m["id"]) == id {
			return false
		}
	}
	return true
}

func TestCodexDriverNeverAutoApproves(t *testing.T) {
	k := approvalKit(t)
	d := k.driver()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	req := nextApproval(t, d)
	if req.ID == "" || req.JobID != res.JobID || req.Kind != provider.ApprovalKindCommand {
		t.Fatalf("approval request = %+v", req)
	}
	time.Sleep(300 * time.Millisecond)
	if !noAnswer(k.written(), "100") {
		t.Fatal("the driver answered an approval without ResolveApproval")
	}
	// A double that auto-answers is caught by the same check.
	if noAnswer([]map[string]json.RawMessage{{"id": json.RawMessage("100"), "result": json.RawMessage(`{}`)}}, "100") {
		t.Fatal("the no-answer check cannot fail")
	}
	if err := d.ResolveApproval(req.ID, "bridge-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collect(testCtx(t), res.JobID); err != nil {
		t.Fatalf("Collect after accept: %v", err)
	}
	ans := k.answersTo("100")
	if len(ans) != 1 || string(ans[0]["result"]) != `{"decision":"accept"}` {
		t.Fatalf("answers = %v", ans)
	}
	if err := d.ResolveApproval(req.ID, "again"); err == nil {
		t.Fatal("a second answer was accepted")
	}
}

// assertFailClosed checks one decline, a turn/interrupt, no accept and a
// cancelled job whose Collect carries want.
func assertFailClosed(t *testing.T, k *kit, id provider.AgentJobID, want error) {
	t.Helper()
	d := k.driver()
	_, err := d.Collect(testCtx(t), id)
	mustSentinel(t, err, want)
	if st, _ := d.Status(testCtx(t), id); st != provider.AgentRunCancelled {
		t.Fatalf("state %q, want cancelled", st)
	}
	ans := k.answersTo("100")
	if len(ans) != 1 || string(ans[0]["result"]) != `{"decision":"decline"}` {
		t.Fatalf("answers = %v, want one decline", ans)
	}
	if !k.wroteMethod("turn/interrupt") {
		t.Fatal("no turn/interrupt was sent")
	}
}

func TestCodexApprovalTimeoutCancelsFailClosed(t *testing.T) {
	k := approvalKit(t)
	k.cfg.Defaults.ApprovalTimeout = 150 * time.Millisecond
	d := k.driver()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	req := nextApproval(t, d)
	assertFailClosed(t, k, res.JobID, ErrApprovalTimeout)
	if err := d.ResolveApproval(req.ID, "late-token"); err == nil {
		t.Fatal("a late accept after the timeout was taken")
	}
}

func TestCodexApprovalDenialCancelsFailClosed(t *testing.T) {
	k := approvalKit(t)
	d := k.driver()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	req := nextApproval(t, d)
	if err := d.ResolveApproval(req.ID, ""); err != nil {
		t.Fatal(err)
	}
	assertFailClosed(t, k, res.JobID, ErrApprovalDenied)
}

func TestCodexDataClassJoined(t *testing.T) {
	k := approvalKit(t)
	d := k.driver()
	spec := k.spec()
	spec.DataClass = provider.DataClassRestricted // envelope is internal
	res, err := d.Spawn(testCtx(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	req := nextApproval(t, d)
	if req.DataClass != provider.DataClassRestricted {
		t.Fatalf("approval tier %q", req.DataClass)
	}
	if err := d.ResolveApproval(req.ID, "tok"); err != nil {
		t.Fatal(err)
	}
	cr, err := d.Collect(testCtx(t), res.JobID)
	if err != nil || cr.DataClass != provider.DataClassRestricted {
		t.Fatalf("Collect = %+v, %v", cr, err)
	}
	evs, _ := d.Events(res.JobID)
	for _, ev := range evs {
		if ev.DataClass != provider.DataClassRestricted {
			t.Fatalf("event %s tier %q", ev.Native.Type, ev.DataClass)
		}
	}
	envTier := spec
	envTier.DataClass, envTier.Envelope.Sensitivity = provider.DataClassPublic, provider.SensitivityRestricted
	if j := newJob("x", envTier, ""); j.tier != provider.DataClassRestricted {
		t.Fatalf("envelope tier not joined: %q", j.tier)
	}
}

func TestCodexSendsInitializedAndJournalsUserAgent(t *testing.T) {
	k := newKit(t)
	res, _, err := k.run(k.spec())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range k.written() {
		var method string
		_ = json.Unmarshal(m["method"], &method)
		order = append(order, method)
	}
	if strings.Join(order, ",") != "initialize,initialized,thread/start,turn/start" {
		t.Fatalf("client frames = %q", order)
	}
	var init struct {
		Params struct {
			ClientInfo struct{ Name, Version string } `json:"clientInfo"`
		} `json:"params"`
	}
	b, _ := json.Marshal(k.written()[0])
	_ = json.Unmarshal(b, &init)
	if init.Params.ClientInfo.Name != ClientName || init.Params.ClientInfo.Version == "" {
		t.Fatalf("clientInfo = %+v", init.Params.ClientInfo)
	}
	jr, _ := k.driver().Journal(res.JobID)
	if !strings.HasPrefix(jr.UserAgent, "cascade/0.144.2 ") {
		t.Fatalf("user agent %q not journaled", jr.UserAgent)
	}
}

func TestCodexHomeMatchesProfileAuthDir(t *testing.T) {
	k := newKit(t)
	k.opts.CodexHome = "/some/other/codex-home"
	res, _, err := k.run(k.spec())
	if err != ErrCodexHomeMismatch {
		t.Fatalf("Collect = %v, want ErrCodexHomeMismatch", err)
	}
	if k.wroteMethod("thread/start") {
		t.Fatal("thread/start was sent after a codexHome mismatch")
	}
	if st, _ := k.driver().Status(testCtx(t), res.JobID); st != provider.AgentRunFailed {
		t.Fatalf("state %q", st)
	}
	k2 := newKit(t)
	spec := k2.spec()
	spec.Env.Driver = spec.Env.Driver[:2] // no CODEX_HOME
	if _, err := k2.driver().Spawn(testCtx(t), spec); err != ErrCodexHomeMismatch {
		t.Fatalf("Spawn without CODEX_HOME = %v", err)
	}
}
