package codex

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestCodexModelOpsRefused(t *testing.T) {
	d, err := New(Config{Binary: "/usr/bin/codex"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	if _, err := d.Chat(ctx, provider.ChatRequest{}); err != errUnsupportedModelOp {
		t.Errorf("Chat = %v", err)
	}
	if _, err := d.Embed(ctx, provider.ModelEmbedRequest{}); err != errUnsupportedModelOp {
		t.Errorf("Embed = %v", err)
	}
	if _, err := d.Count(ctx, provider.CountRequest{}); err != errUnsupportedModelOp {
		t.Errorf("Count = %v", err)
	}
	if err := d.Stream(ctx, provider.ChatRequest{}, nil); err != errUnsupportedModelOp {
		t.Errorf("Stream = %v", err)
	}
	if err := d.ResolveApproval("nope", "tok"); err == nil {
		t.Error("ResolveApproval(unknown) = nil")
	}
}

func TestCodexUnknownJobs(t *testing.T) {
	d, err := New(Config{Binary: "/usr/bin/codex"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	const id = provider.AgentJobID("missing")
	if _, err := d.Status(ctx, id); err != provider.ErrJobNotFound {
		t.Errorf("Status = %v", err)
	}
	if _, err := d.Collect(ctx, id); err != provider.ErrJobNotFound {
		t.Errorf("Collect = %v", err)
	}
	if _, err := d.Artifacts(ctx, id); err != provider.ErrJobNotFound {
		t.Errorf("Artifacts = %v", err)
	}
	if _, err := d.Events(id); err != provider.ErrJobNotFound {
		t.Errorf("Events = %v", err)
	}
	if _, err := d.Journal(id); err != provider.ErrJobNotFound {
		t.Errorf("Journal = %v", err)
	}
	if _, err := d.ProcessGroup(id); err != provider.ErrJobNotFound {
		t.Errorf("ProcessGroup = %v", err)
	}
	if err := d.Cancel(ctx, id); err != provider.ErrJobNotFound {
		t.Errorf("Cancel = %v", err)
	}
	if v := versionFromUserAgent("no-slash"); v != "" {
		t.Errorf("version of a bare agent = %q", v)
	}
}

func TestCodexCancelAnswersPendingApproval(t *testing.T) {
	k := approvalKit(t)
	d := k.driver()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	_ = nextApproval(t, d)
	if err := d.Cancel(testCtx(t), res.JobID); err != nil {
		t.Fatal(err)
	}
	ans := k.answersTo("100")
	if len(ans) != 1 || string(ans[0]["result"]) != `{"decision":"cancel"}` {
		t.Fatalf("answers = %v, want one cancel", ans)
	}
}

func TestCodexTurnStatusMapping(t *testing.T) {
	for status, want := range map[string]provider.AgentRunState{
		"completed": provider.AgentRunAccepted, "interrupted": provider.AgentRunCancelled,
		"failed": provider.AgentRunFailed, "": provider.AgentRunFailed,
	} {
		j := newJob("j", provider.AgentJobSpec{}, "")
		j.turnStatus = status
		j.settleTurn()
		if j.state != want {
			t.Errorf("turn status %q -> %q, want %q", status, j.state, want)
		}
	}
	k := newKit(t)
	lines := loadSession(t)
	last := len(lines) - 1
	lines[last] = s2c(400, `{"method":"turn/completed","params":{"turn":{"id":"0","status":"failed","error":{"message":"x"}}}}`)
	k.useSession(lines)
	_, _, err := k.run(k.spec())
	if err != ErrTurnFailed {
		t.Fatalf("Collect = %v, want ErrTurnFailed", err)
	}
}
