package executive

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func publishExec(t *testing.T, bus *events.Bus, stream StreamID) events.Event {
	t.Helper()
	ev, err := bus.PublishEvent(context.Background(), ExecNamespace,
		events.Event{Kind: events.EventKind(KindTaskStarted), StreamID: string(stream), Source: "test"})
	if err != nil {
		t.Fatalf("PublishEvent(%q): %v", stream, err)
	}
	return ev
}

// TestEXE01LegacyAC4 verifies the bus, not the executive, allocates the
// per-stream sequence and recovers its head after a restart.
func TestEXE01LegacyAC4(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	bus := events.New(store, clock)

	run, err := DeriveStreamID("run-1", "")
	if err != nil || run != "run-1" {
		t.Fatalf("DeriveStreamID(run-1) = %q, %v", run, err)
	}
	for want := uint64(1); want <= 3; want++ {
		if ev := publishExec(t, bus, run); ev.StreamSeq != want || ev.StreamID != "run-1" {
			t.Fatalf("publish %d: stream %q seq %d", want, ev.StreamID, ev.StreamSeq)
		}
	}
	proj, err := DeriveStreamID("", "p1")
	if err != nil || proj != "project:p1" {
		t.Fatalf("DeriveStreamID(project p1) = %q, %v", proj, err)
	}
	if ev := publishExec(t, bus, proj); ev.StreamSeq != 1 {
		t.Fatalf("project:p1 first seq = %d, want 1", ev.StreamSeq)
	}
	_, err = bus.PublishEvent(ctx, ExecNamespace, events.Event{Kind: "exec.TASK_STARTED", StreamID: "run-1", StreamSeq: 9})
	if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
		t.Fatalf("draft with StreamSeq 9: err %v kind %v, want KindInvalidInput", err, k)
	}

	restarted := events.New(store, clock)
	head, err := StreamHead(ctx, restarted, run)
	if err != nil || head != 3 {
		t.Fatalf("StreamHead after restart = %d, %v; want 3", head, err)
	}
	if ev := publishExec(t, restarted, run); ev.StreamSeq != 4 {
		t.Fatalf("next seq after restart = %d, want 4", ev.StreamSeq)
	}

	if _, err := DeriveStreamID("", ""); !errors.Is(err, ErrNoStreamIdentity) {
		t.Fatalf("DeriveStreamID empty err = %v", err)
	}
	if _, err := StreamHead(ctx, nil, run); !errors.Is(err, ErrNoStreamIdentity) {
		t.Fatalf("StreamHead nil bus err = %v", err)
	}
	if _, err := StreamHead(ctx, restarted, ""); !errors.Is(err, ErrNoStreamIdentity) {
		t.Fatalf("StreamHead empty stream err = %v", err)
	}
	assertNoTypeOrVar(t, "sequence.go")
}

func assertNoTypeOrVar(t *testing.T, path string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && (gd.Tok == token.TYPE || gd.Tok == token.VAR) {
			t.Errorf("%s declares a %s at offset %d", path, gd.Tok, gd.Pos())
		}
	}
}

// TestStreamHeadUnavailableAfterClose verifies a closed bus error passes through.
func TestStreamHeadUnavailableAfterClose(t *testing.T) {
	isolateHome(t)
	bus := events.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(0, 0)))
	if err := bus.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := StreamHead(context.Background(), bus, "run-1")
	if k, _ := cascade.KindOf(err); k != cascade.KindUnavailable {
		t.Fatalf("StreamHead on closed bus: err %v kind %v, want KindUnavailable", err, k)
	}
}
