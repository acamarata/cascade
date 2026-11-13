package attempt

import (
	"errors"
	"strings"
	"sync"
)

// AttemptRecord holds the six canonical fields describing a single attempt.
type AttemptRecord struct { //nolint:revive // name fixed by the ticket contract
	Lane, Model, Node, Outcome, Cause, SHA string
}

// Sink accepts attempt records and can be closed.
type Sink interface {
	Record(AttemptRecord)
	Close() error
}

// OverflowSink extends Sink with overflow escalation capability.
type OverflowSink interface {
	Sink
	RecordOverflow(OverflowRow) error
}

type overflowSink struct {
	base      Sink
	dir       string
	appendRow func(string, OverflowRow) error
	ch        chan OverflowRow
	mu        sync.Mutex
	closed    bool
	done      chan struct{}
	closeErr  error
	closeOnce sync.Once
}

func newOverflowSink(base Sink, dir string, appendRow func(string, OverflowRow) error) (OverflowSink, error) {
	if base == nil || strings.TrimSpace(dir) == "" || appendRow == nil {
		return nil, ErrInvalidOverflow
	}
	s := &overflowSink{
		base:      base,
		dir:       dir,
		appendRow: appendRow,
		ch:        make(chan OverflowRow, 1024),
		done:      make(chan struct{}),
	}
	go s.worker()
	return s, nil
}

// NewOverflowSink creates an OverflowSink that delegates to base and appends
// overflow rows via AppendOverflowRow into dir.
func NewOverflowSink(base Sink, dir string) (OverflowSink, error) {
	return newOverflowSink(base, dir, AppendOverflowRow)
}

func (s *overflowSink) Record(r AttemptRecord) {
	s.base.Record(r)
}

func (s *overflowSink) RecordOverflow(row OverflowRow) error {
	if err := validateOverflowRow(row); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrOverflowClosed
	}
	select {
	case s.ch <- row:
		s.mu.Unlock()
		return nil
	default:
		s.mu.Unlock()
		return ErrOverflowQueueFull
	}
}

func (s *overflowSink) worker() {
	for row := range s.ch {
		_ = s.appendRow(s.dir, row)
	}
	close(s.done)
}

func (s *overflowSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return s.closeErr
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()

	<-s.done

	s.closeOnce.Do(func() {
		s.closeErr = errors.Join(s.base.Close())
	})
	return s.closeErr
}
