package background

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

type fakeClock struct {
	ch        chan time.Time
	stopCalls int
}

func (f *fakeClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	if d != 10*time.Second {
		panic("unexpected duration")
	}
	return f.ch, func() { f.stopCalls++ }
}

type fakeReader struct {
	calls int
}

func (f *fakeReader) ReadNode(ctx context.Context, nodeID, pos string) (string, error) {
	f.calls++
	return "v", nil
}

type errReader struct{}

func (e *errReader) ReadNode(ctx context.Context, nodeID, pos string) (string, error) {
	return "", errors.New("fail")
}

type fakeReporter struct {
	calls int
}

func (f *fakeReporter) Unreadable(nodeID string, err error) {
	f.calls++
}

// TestPollInterval verifies ticker lifecycle and polling cycles.
func TestPollInterval(t *testing.T) {
	t.Run("three ticks cause three cycles with no initial poll", func(t *testing.T) {
		clk := &fakeClock{ch: make(chan time.Time)}
		rdr := &fakeReader{}
		rpt := &fakeReporter{}
		p := &Poller{
			Clock:     clk,
			Reader:    rdr,
			Reporter:  rpt,
			Nodes:     []string{"n1"},
			Positions: map[string]string{},
		}
		done := make(chan int)
		go func() { done <- p.Run(context.Background()) }()

		clk.ch <- time.Now().Add(10 * time.Second)
		clk.ch <- time.Now().Add(20 * time.Second)
		clk.ch <- time.Now().Add(30 * time.Second)

		time.Sleep(50 * time.Millisecond)
		if rdr.calls != 3 {
			t.Fatalf("expected 3 reads, got %d", rdr.calls)
		}
		close(clk.ch)
		code := <-done
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
		if clk.stopCalls != 1 {
			t.Fatalf("expected stop called once, got %d", clk.stopCalls)
		}
	})

	t.Run("empty nodes is valid", func(t *testing.T) {
		clk := &fakeClock{ch: make(chan time.Time)}
		rdr := &fakeReader{}
		rpt := &fakeReporter{}
		p := &Poller{
			Clock:    clk,
			Reader:   rdr,
			Reporter: rpt,
			Nodes:    []string{},
		}
		done := make(chan int)
		go func() { done <- p.Run(context.Background()) }()
		close(clk.ch)
		code := <-done
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
}

// TestPollInvalidConfiguration verifies that invalid configs return 1 without side effects.
func TestPollInvalidConfiguration(t *testing.T) {
	validClock := &fakeClock{ch: make(chan time.Time)}
	validReader := &fakeReader{}
	validReporter := &fakeReporter{}

	tests := []struct {
		name string
		p    *Poller
		ctx  context.Context
	}{
		{"nil receiver", nil, context.Background()},
		{"nil context", &Poller{Clock: validClock, Reader: validReader, Reporter: validReporter}, nil},
		{"nil clock", &Poller{Reader: validReader, Reporter: validReporter}, context.Background()},
		{"nil reader", &Poller{Clock: validClock, Reporter: validReporter}, context.Background()},
		{"nil reporter", &Poller{Clock: validClock, Reader: validReader}, context.Background()},
		{"blank node id", &Poller{Clock: validClock, Reader: validReader, Reporter: validReporter, Nodes: []string{"  "}}, context.Background()},
		{"duplicate node ids", &Poller{Clock: validClock, Reader: validReader, Reporter: validReporter, Nodes: []string{"a", "a"}}, context.Background()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := tt.p.Run(tt.ctx)
			if code != 1 {
				t.Fatalf("expected exit 1, got %d", code)
			}
		})
	}

	t.Run("nil ticker channel returns 1", func(t *testing.T) {
		nc := &nilChanClock{}
		rdr := &fakeReader{}
		rpt := &fakeReporter{}
		p := &Poller{Clock: nc, Reader: rdr, Reporter: rpt, Nodes: []string{"n1"}}
		code := p.Run(context.Background())
		if code != 1 {
			t.Fatalf("expected exit 1, got %d", code)
		}
		if nc.stopCalls != 0 {
			t.Fatalf("stop should not be called when ch is nil")
		}
	})

	t.Run("nil stop callback returns 1", func(t *testing.T) {
		ns := &nilStopClock{ch: make(chan time.Time)}
		rdr := &fakeReader{}
		rpt := &fakeReporter{}
		p := &Poller{Clock: ns, Reader: rdr, Reporter: rpt, Nodes: []string{"n1"}}
		code := p.Run(context.Background())
		if code != 1 {
			t.Fatalf("expected exit 1, got %d", code)
		}
	})

	t.Run("invalid config does not mutate positions or call reader/reporter", func(t *testing.T) {
		rdr := &fakeReader{}
		rpt := &fakeReporter{}
		p := &Poller{
			Clock:     validClock,
			Reader:    rdr,
			Reporter:  rpt,
			Nodes:     []string{"a", "a"},
			Positions: map[string]string{"x": "y"},
		}
		p.Run(context.Background())
		if rdr.calls != 0 {
			t.Fatal("reader should not be called")
		}
		if rpt.calls != 0 {
			t.Fatal("reporter should not be called")
		}
		if v, ok := p.Positions["x"]; !ok || v != "y" {
			t.Fatal("positions mutated")
		}
	})
}

type nilChanClock struct{ stopCalls int }

func (n *nilChanClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	return nil, func() { n.stopCalls++ }
}

type nilStopClock struct {
	ch chan time.Time
}

func (n *nilStopClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	return n.ch, nil
}

var _ os.Signal = (*testSignal)(nil)

type testSignal struct{}

func (testSignal) String() string { return "test" }
func (testSignal) Signal()        {}
