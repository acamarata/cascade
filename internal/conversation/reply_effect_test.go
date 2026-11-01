package conversation

// Purpose: a reply that does not finish stores nothing and reports no
//   success, and its effect record says so; the SEC-33 crash recipe leaves
//   an intent that recovery marks unknown-outcome and a retry never reuses.
// Inputs: CASCADE_REPLY_EFFECT_CHILD=<dir> makes this binary the child.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

const (
	replyChildEnv   = "CASCADE_REPLY_EFFECT_CHILD"
	replyCrashCode  = 3
	replyCrashTurn  = "crash user turn"
	replyCrashReply = "crash reply"
)

// wantCascade asserts err is a *cascade.Error of kind with message msg.
func wantCascade(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != kind || ce.Msg != msg {
		t.Fatalf("err = %v, want %s %q", err, kind, msg)
	}
}

// assertNoReply checks nothing was stored for user and key ended failed.
func assertNoReply(t *testing.T, f *replyFixture, user Turn, key string) {
	t.Helper()
	if n := len(storedTurns(t, f.store, user.ThreadID)); n != 1 {
		t.Fatalf("turns = %d, want only the user turn", n)
	}
	if _, ok, _ := f.store.(*conversationStore).replyFor(context.Background(), user.ID); ok {
		t.Fatal("a reply mapping was stored for an unfinished reply")
	}
	if phase, found, err := f.effects.EffectState(context.Background(), key); err != nil || !found || phase != audit.EffectFailed {
		t.Fatalf("effect %s = %q %v %v, want failed", key, phase, found, err)
	}
}

func TestReplyCancelAfterFirstTokenNoSuccess(t *testing.T) {
	door := newScriptDoor("partial", " rest")
	door.holdAfterFirst = true
	f := newReplyFixture(t, door, 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "hi")
	ctx, cancel := context.WithCancel(ownerCLICtx())
	defer cancel()
	id, err := f.replier.open(ctx, "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := f.hub.Subscribe(context.Background(), id, ownerCaller())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		res, err := f.replier.Reply(ctx, ReplyRequest{StreamID: id})
		if err == nil {
			err = errors.New("reported success: " + res.AssistantTurnID)
		}
		done <- err
	}()
	if first := <-frames; first.Type != FrameDelta || first.Delta != "partial" {
		t.Fatalf("first frame = %+v", first)
	}
	cancel()
	wantCascade(t, <-done, cascade.KindCanceled, "conversation: reply cancelled; no assistant turn stored")
	assertNoReply(t, f, user, replyEffectPrefix+user.ID+":1")
	if last := lastFrame(frames); last.Type != FrameError || last.Kind != cascade.KindCanceled.String() || last.Delta != "" {
		t.Fatalf("terminal frame = %+v, want an error frame naming canceled", last)
	}
}

func TestReplyProviderErrorNoTurn(t *testing.T) {
	door := newScriptDoor("partial")
	door.events = []provider.StreamEvent{{Kind: provider.StreamEventDelta, Delta: "partial"},
		{Kind: provider.StreamEventError, Err: cascade.New(cascade.KindUnavailable, "lane down")}}
	f := newReplyFixture(t, door, 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "hi")
	_, err := f.openAndReply(t, ownerCLICtx(), "th", user.ID)
	wantCascade(t, err, cascade.KindUnavailable, "conversation: model stream failed; no assistant turn stored")
	assertNoReply(t, f, user, replyEffectPrefix+user.ID+":1")
	// A dispatch refused outright fails the next attempt the same way.
	door.streamErr = cascade.New(cascade.KindQuotaExhausted, "quota spent")
	_, err = f.openAndReply(t, ownerCLICtx(), "th", user.ID)
	wantCascade(t, err, cascade.KindQuotaExhausted, "quota spent")
	assertNoReply(t, f, user, replyEffectPrefix+user.ID+":2")
}

// lastFrame drains frames and returns the final one.
func lastFrame(frames <-chan Frame) Frame {
	var last Frame
	for f := range frames {
		last = f
	}
	return last
}

// crashJournal exits the process at the reply's journal intent: after the
// provider call, before the persist and the confirm.
type crashJournal struct{ JournalStore }

func (c crashJournal) Append(ctx context.Context, entityID string, kind journal.Kind, op string, payload json.RawMessage) (journal.Entry, error) {
	if kind == journal.KindIntent {
		os.Exit(replyCrashCode)
	}
	return c.JournalStore.Append(ctx, entityID, kind, op, payload)
}

// loggingDoor records each main dispatch in dir/provider.log.
type loggingDoor struct {
	*scriptDoor
	dir string
}

func (d loggingDoor) ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, func(), error) {
	f, err := os.OpenFile(filepath.Join(d.dir, "provider.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	_, err = f.WriteString("dispatch\n")
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, nil, err
	}
	return d.scriptDoor.ExecuteStream(ctx, req)
}

// openCrashStores opens dir's conversation db and its runtime store; the
// returned func closes both.
func openCrashStores(t *testing.T, dir string) (Store, *sqlite.Driver, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatal(err)
	}
	if err := ensureScopeSchema(ctx, db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	rt, err := sqlite.Open(ctx, filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db), rt, func() { _ = rt.Close(); _ = db.Close() }
}

// seedCrashStores creates dir's stores with the thread and its user turn,
// then closes them before the child opens the same files.
func seedCrashStores(t *testing.T, dir string) {
	t.Helper()
	store, _, closeAll := openCrashStores(t, dir)
	defer closeAll()
	seedOwnedThread(t, store, "th", provider.SensitivityRestricted)
	seedReplyTurn(t, store, "th", RoleUser, OriginLocal, "", replyCrashTurn)
}

// crashFixture builds a Replier over dir's stores with the journal wrapped
// and every main dispatch logged to dir/provider.log.
func crashFixture(t *testing.T, dir string, wrap func(JournalStore) JournalStore) (*replyFixture, Turn) {
	t.Helper()
	store, rt, closeAll := openCrashStores(t, dir)
	t.Cleanup(closeAll)
	clock := testkit.NewFrozenClock(replyTestInstant)
	effects, err := audit.NewEffectLog(audit.New(rt, clock, nil))
	if err != nil {
		t.Fatal(err)
	}
	js := journal.New(rt, clock, "conversation.journal")
	door := newScriptDoor(replyCrashReply)
	f := newReplyFixtureWith(t, store, wrap(js), effects, door, clock, 4000)
	f.replier.door = tieredDoor{next: loggingDoor{scriptDoor: door, dir: dir}}
	return f, storedTurns(t, store, "th")[0]
}

func TestReplyEffectCrashInjection(t *testing.T) {
	if dir := os.Getenv(replyChildEnv); dir != "" {
		f, user := crashFixture(t, dir, func(js JournalStore) JournalStore { return crashJournal{js} })
		_, _ = f.openAndReply(t, ownerCLICtx(), "th", user.ID)
		os.Exit(8) // the journal intent must have ended the process
	}
	dir := t.TempDir()
	seedCrashStores(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplyEffectCrashInjection$", "-test.count=1")
	cmd.Env = append(os.Environ(), replyChildEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != replyCrashCode {
		t.Fatalf("child = %v, want exit status %d; output:\n%s", err, replyCrashCode, out)
	}
	f, user := crashFixture(t, dir, func(js JournalStore) JournalStore { return js })
	key1, key2 := replyEffectPrefix+user.ID+":1", replyEffectPrefix+user.ID+":2"
	if phase, found, err := f.effects.EffectState(ctx, key1); err != nil || !found || phase != audit.EffectIntent {
		t.Fatalf("after the crash %s = %q %v %v, want intent", key1, phase, found, err)
	}
	if err := f.replier.RecoverPending(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := f.openAndReply(t, ownerCLICtx(), "th", user.ID)
	if err != nil || res.Replayed {
		t.Fatalf("retry = %+v, %v; want a fresh reply", res, err)
	}
	for key, want := range map[string]audit.EffectPhase{key1: audit.EffectUnknown, key2: audit.EffectConfirmed} {
		if phase, _, err := f.effects.EffectState(ctx, key); err != nil || phase != want {
			t.Fatalf("%s = %q (%v), want %q", key, phase, err, want)
		}
	}
	ran, err := os.ReadFile(filepath.Join(dir, "provider.log"))
	if err != nil || strings.Count(string(ran), "dispatch\n") != 2 {
		t.Fatalf("provider dispatches %q (%v), want one in the child and one retry", ran, err)
	}
}
