package background

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type callRec struct {
	nodeID, pos string
}

type recReader struct {
	mu      sync.Mutex
	calls   []callRec
	results map[string]struct {
		pos string
		err error
	}
}

// ReadNode records the call and returns preconfigured results.
func (r *recReader) ReadNode(ctx context.Context, nodeID, pos string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, callRec{nodeID, pos})
	r.mu.Unlock()
	if res, ok := r.results[nodeID]; ok {
		return res.pos, res.err
	}
	return "", nil
}

type fnRdr struct {
	fn func(context.Context, string, string) (string, error)
}

// ReadNode delegates to the wrapped function.
func (f *fnRdr) ReadNode(ctx context.Context, n, p string) (string, error) { return f.fn(ctx, n, p) }

type tClock struct{ ch <-chan time.Time }

// NewTicker returns the preconfigured channel and a no-op stop function.
func (c *tClock) NewTicker(d time.Duration) (<-chan time.Time, func()) { return c.ch, func() {} }

type tRep struct{}

// Unreadable is a no-op for testing.
func (r *tRep) Unreadable(nodeID string, err error) {}

// TestPerNodePosition verifies per-node independent positions in input order.
func TestPerNodePosition(t *testing.T) {
	reader := &recReader{
		results: map[string]struct {
			pos string
			err error
		}{
			"nodeA": {"newA", nil},
			"nodeB": {"", errors.New("fail")},
		},
	}
	ch := make(chan time.Time, 1)
	p := &Poller{
		Nodes:       []string{"nodeA", "nodeB"},
		Positions:   map[string]string{"nodeA": "posA", "unrelated": "keep-me"},
		Clock:       &tClock{ch: ch},
		Reader:      reader,
		Reporter:    &tRep{},
		FleetCursor: "fleet-original",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	ch <- time.Now()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if len(reader.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(reader.calls))
	}
	if reader.calls[0].nodeID != "nodeA" || reader.calls[0].pos != "posA" {
		t.Errorf("call0: got %s/%s", reader.calls[0].nodeID, reader.calls[0].pos)
	}
	if reader.calls[1].nodeID != "nodeB" || reader.calls[1].pos != "" {
		t.Errorf("call1: got %s/%q", reader.calls[1].nodeID, reader.calls[1].pos)
	}
	if p.Positions["nodeA"] != "newA" {
		t.Errorf("nodeA pos: got %s", p.Positions["nodeA"])
	}
	if p.Positions["nodeB"] != "" {
		t.Errorf("nodeB pos: got %s", p.Positions["nodeB"])
	}
	if p.Positions["unrelated"] != "keep-me" {
		t.Errorf("unrelated: got %s", p.Positions["unrelated"])
	}
}

// TestPollFleetCursorUntouched verifies FleetCursor is never modified by Run.
func TestPollFleetCursorUntouched(t *testing.T) {
	var called bool
	reader := &fnRdr{fn: func(ctx context.Context, n, p string) (string, error) {
		called = true
		return "new-pos", nil
	}}
	ch := make(chan time.Time, 1)
	p := &Poller{
		Nodes:       []string{"testNode"},
		Positions:   map[string]string{"testNode": "init"},
		Clock:       &tClock{ch: ch},
		Reader:      reader,
		Reporter:    &tRep{},
		FleetCursor: "fleet-original",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	ch <- time.Now()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if p.FleetCursor != "fleet-original" {
		t.Errorf("FleetCursor changed: got %s", p.FleetCursor)
	}
	if !called {
		t.Error("reader was not called")
	}
}
