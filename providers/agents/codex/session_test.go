package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

const threadID = "019a6f10-3c2e-7d41-9b8a-5e0c2f7d1a42"

func TestCodexOutputFilterErrorWithholds(t *testing.T) {
	k := newKit(t)
	spec := k.spec()
	spec.Filter = func(b []byte) ([]byte, error) {
		if bytes.Contains(b, []byte(`"agentMessage"`)) {
			return nil, errors.New("substitution failed")
		}
		return b, nil
	}
	res, cr, err := k.run(spec)
	if err != provider.ErrEgressSubstitutionFailed {
		t.Fatalf("Collect = %v, want ErrEgressSubstitutionFailed", err)
	}
	if cr.Output != "" {
		t.Fatalf("output not withheld: %q", cr.Output)
	}
	if st, _ := k.driver().Status(testCtx(t), res.JobID); st != provider.AgentRunFailed {
		t.Fatalf("state %q, want failed", st)
	}
}

func TestCodexErrorPathsTyped(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		k := newKit(t)
		k.cfg.Binary = k.root + "/no-such-codex"
		_, err := k.driver().Spawn(testCtx(t), k.spec())
		mustSentinel(t, err, ErrStart)
	})
	t.Run("malformed frame", func(t *testing.T) {
		k := newKit(t)
		k.useSession(beforeTurnCompleted(loadSession(t), sessionLine{Dir: "s2c", TMS: 350, Raw: "not json at all"}))
		_, _, err := k.run(k.spec())
		mustSentinel(t, err, ErrMalformedFrame)
	})
	t.Run("shape violation", func(t *testing.T) {
		k := newKit(t)
		k.useSession(beforeTurnCompleted(loadSession(t), s2c(350, `{"id":5}`)))
		_, _, err := k.run(k.spec())
		mustSentinel(t, err, ErrMalformedFrame)
	})
	t.Run("non-zero exit", func(t *testing.T) {
		k := newKit(t)
		lines := loadSession(t)
		k.useSession(lines[:2]) // initialize answered, then stdout closes
		k.opts.ExitCode = 3
		_, _, err := k.run(k.spec())
		mustSentinel(t, err, ErrNonZeroExit)
	})
	errorPathsServerAndCancel(t)
	t.Run("mcp-server", func(t *testing.T) {
		mustSentinel(t, ValidateLaunch([]string{"codex", "mcp-server"}), ErrForbiddenLaunchPath)
	})
}

// errorPathsServerAndCancel holds the server-error and cancel legs.
func errorPathsServerAndCancel(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		k := newKit(t)
		lines := loadSession(t)
		lines[5] = s2c(41, `{"id":2,"error":{"code":-32600,"message":"bad"}}`)
		k.useSession(lines)
		_, _, err := k.run(k.spec())
		mustSentinel(t, err, ErrServerError)
	})
	t.Run("cancel", func(t *testing.T) {
		k := newKit(t)
		d := k.driver()
		res, err := d.Spawn(testCtx(t), k.spec())
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Cancel(testCtx(t), res.JobID); err != nil {
			t.Fatal(err)
		}
		_, err = d.Collect(testCtx(t), res.JobID)
		mustSentinel(t, err, ErrRunCancelled)
		if err := d.Cancel(testCtx(t), res.JobID); err != nil {
			t.Fatalf("Cancel(terminal) = %v", err)
		}
	})
}

func TestCodexProtocolPinRefusal(t *testing.T) {
	d, err := New(Config{Binary: "/usr/bin/codex"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.Negotiate(testCtx(t), provider.ProtocolRange{Min: "1", Max: "1"}); err != nil || v != "1" {
		t.Fatalf("Negotiate(1) = %q, %v", v, err)
	}
	if _, err := d.Negotiate(testCtx(t), provider.ProtocolRange{Min: "2", Max: "3"}); err != provider.ErrHarnessIncompatible {
		t.Fatalf("Negotiate(2..3) = %v", err)
	}
	k := newKit(t)
	lines := loadSession(t)
	lines[1] = s2c(3, `{"id":1,"result":{"userAgent":"cascade/0.200.0 (Linux; x86_64)","codexHome":"/x"}}`)
	k.useSession(lines)
	_, _, err = k.run(k.spec())
	if err != provider.ErrHarnessIncompatible {
		t.Fatalf("Collect against codex 0.200.0 = %v, want ErrHarnessIncompatible", err)
	}
}

func TestCodexRejectsForeignSocketFrame(t *testing.T) {
	k := newKit(t)
	k.useSession(beforeTurnCompleted(loadSession(t), s2c(350, `{"id":99,"result":{}}`)))
	_, _, err := k.run(k.spec())
	if err != ErrForeignFrame {
		t.Fatalf("Collect = %v, want ErrForeignFrame", err)
	}
}

func TestCodexEventsDedupAndOrder(t *testing.T) {
	j := newJob("j", provider.AgentJobSpec{DataClass: provider.DataClassInternal}, "")
	if err := j.record(provider.DriverEvent{Kind: provider.DriverEventStatus, Seq: 1, DedupKey: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := j.record(provider.DriverEvent{Kind: provider.DriverEventStatus, Seq: 2, DedupKey: "x"}); err != nil || len(j.events) != 1 {
		t.Fatalf("duplicate kept: %v, %d events", err, len(j.events))
	}
	if err := j.record(provider.DriverEvent{Kind: provider.DriverEventStatus, Seq: 1, DedupKey: "y"}); err != provider.ErrOutOfOrderEvent {
		t.Fatalf("regressing Seq = %v, want ErrOutOfOrderEvent", err)
	}
	k := newKit(t)
	dup := `{"method":"item/completed","params":{"turnId":"0","item":{"type":"reasoning","id":"item-1"}}}`
	k.useSession(beforeTurnCompleted(loadSession(t), s2c(350, dup)))
	res, _, err := k.run(k.spec())
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := k.driver().Events(res.JobID)
	seen := 0
	for i, ev := range evs {
		if i > 0 && ev.Seq <= evs[i-1].Seq {
			t.Fatalf("event %d Seq %d does not increase", i, ev.Seq)
		}
		if ev.DedupKey == "item/completed:item-1" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("item-1 completion recorded %d times, want 1", seen)
	}
}

func TestCodexCancelConfirmsExit(t *testing.T) {
	k := newKit(t)
	k.opts.Hang = true
	k.cfg.Defaults.CancelGrace = 100 * time.Millisecond
	d := k.driver()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	if res.ProcessGroupID <= 1 {
		t.Fatalf("ProcessGroupID %d, want the child's own group", res.ProcessGroupID)
	}
	waitFor(t, "the child to ignore TERM", func() bool {
		_, err := os.Stat(k.codexHome + "/hanging")
		return err == nil
	})
	if err := d.Cancel(testCtx(t), res.JobID); err != nil {
		t.Fatalf("Cancel = %v", err)
	}
	if st, _ := d.Status(testCtx(t), res.JobID); st != provider.AgentRunCancelled {
		t.Fatalf("state %q, want cancelled", st)
	}
	waitFor(t, "the group to be empty", func() bool { return procgroup.Empty(res.ProcessGroupID) })
	if pg, _ := d.ProcessGroup(res.JobID); pg != res.ProcessGroupID {
		t.Fatalf("ProcessGroup = %d", pg)
	}
}

func TestCodexRawItemsPassThrough(t *testing.T) {
	k := newKit(t)
	unmapped := `{"method":"codex/unmapped/notice","params":{"note":"kept"}}`
	k.useSession(beforeTurnCompleted(loadSession(t), s2c(350, unmapped)))
	res, _, err := k.run(k.spec())
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := k.driver().Events(res.JobID)
	found := map[string][]byte{}
	for _, ev := range evs {
		if ev.Native == nil {
			t.Fatal("an event without its native frame")
		}
		found[ev.Native.Type] = ev.Native.Raw
	}
	for _, m := range []string{"account/rateLimits/updated", "codex/unmapped/notice", "thread/tokenUsage/updated", "item/agentMessage/delta", "turn/completed"} {
		if found[m] == nil {
			t.Errorf("%s was dropped", m)
		}
	}
	if string(found["codex/unmapped/notice"]) != unmapped {
		t.Fatalf("raw frame %q, want %q", found["codex/unmapped/notice"], unmapped)
	}
}

func TestCodexThreadIDAndProtocolRecorded(t *testing.T) {
	k := newKit(t)
	res, cr, err := k.run(k.spec())
	if err != nil {
		t.Fatal(err)
	}
	if cr.SessionID != threadID || cr.Output != "ready" {
		t.Fatalf("CollectResult = %+v", cr)
	}
	jr, _ := k.driver().Journal(res.JobID)
	if jr.ThreadID != threadID || jr.TurnID != "0" || jr.SchemaRevision != "1/"+PinnedCodexVersion || jr.CodexVersion != PinnedCodexVersion {
		t.Fatalf("journal = %+v", jr)
	}
	evs, _ := k.driver().Events(res.JobID)
	for _, ev := range evs {
		if ev.Protocol != ProtocolRevision {
			t.Fatalf("event protocol %q", ev.Protocol)
		}
	}
}

func TestCodexUnknownFieldsTolerated(t *testing.T) {
	k := newKit(t)
	lines := loadSession(t)
	for i, l := range lines {
		if l.Dir == "s2c" && l.Frame != nil {
			var m map[string]any
			_ = json.Unmarshal(l.Frame, &m)
			m["futureField"] = map[string]any{"x": 1}
			if p, ok := m["params"].(map[string]any); ok {
				p["anotherNewField"] = []int{1, 2}
			}
			lines[i].Frame, _ = json.Marshal(m)
		}
	}
	k.useSession(lines)
	res, cr, err := k.run(k.spec())
	if err != nil || cr.Output != "ready" {
		t.Fatalf("Collect = %+v, %v", cr, err)
	}
	if st, _ := k.driver().Status(testCtx(t), res.JobID); st != provider.AgentRunAccepted {
		t.Fatalf("state %q", st)
	}
	if !strings.Contains(string(lines[1].Frame), "futureField") {
		t.Fatal("the fixture did not carry the unknown field")
	}
}
