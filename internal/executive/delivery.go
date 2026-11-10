package executive

import (
	"errors"
	"fmt"
	"sync"
)

// DefaultDedupeWindow is the number of delivery keys and EventIDs a
// Deliverer remembers per stream before evicting the oldest.
const DefaultDedupeWindow = 1024

// Delivery and stream-identity errors.
var (
	// ErrInvalidDelivery reports an envelope or argument a Deliverer cannot key.
	ErrInvalidDelivery = errors.New("invalid exec delivery")
	// ErrSequenceConflict reports a sequence that contradicts what the stream already delivered.
	ErrSequenceConflict = errors.New("exec stream sequence conflict")
	// ErrNoStreamIdentity reports an event or call with no derivable stream.
	ErrNoStreamIdentity = errors.New("no exec stream identity")
)

// streamState is one stream's high-water mark and its bounded dedupe memory.
type streamState struct {
	hw       StreamSeq
	keys     map[string]struct{}
	keyOrder []string
	ids      map[string]StreamSeq
	idOrder  []string
}

func newStreamState(hw StreamSeq) *streamState {
	return &streamState{hw: hw, keys: map[string]struct{}{}, ids: map[string]StreamSeq{}}
}

// Deliverer delivers each exec.* envelope at most once per consumer. It is
// keyed by Envelope.IdempotencyKey(Producer) and drops only an exact repeat
// or a retried emission of an already-delivered EventID. It is safe for
// concurrent use.
type Deliverer struct {
	mu      sync.Mutex
	window  int
	streams map[StreamID]*streamState
	dropped uint64
}

// NewDeliverer returns a Deliverer remembering window keys and EventIDs per
// stream. A window below 1 is ErrInvalidDelivery.
func NewDeliverer(window int) (*Deliverer, error) {
	if window < 1 {
		return nil, fmt.Errorf("%w: window %d < 1", ErrInvalidDelivery, window)
	}
	return &Deliverer{window: window, streams: map[StreamID]*streamState{}}, nil
}

// Accept decodes raw with DecodeEnvelope and passes the envelope to Deliver.
func (d *Deliverer) Accept(raw []byte) (*Envelope, bool, error) {
	env, err := DecodeEnvelope(raw)
	if err != nil {
		return nil, false, err
	}
	delivered, err := d.Deliver(env)
	return env, delivered, err
}

// Deliver reports whether env is new and must be handled. It returns
// (false, nil) for an exact repeat or a retried EventID, and
// ErrSequenceConflict when env's sequence contradicts the stream.
func (d *Deliverer) Deliver(env *Envelope) (bool, error) {
	if err := validDelivery(env); err != nil {
		return false, err
	}
	key := env.IdempotencyKey(string(env.Producer))
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.stateLocked(env.StreamID)
	if _, seen := st.keys[key]; seen {
		d.dropped++
		return false, nil
	}
	if seq, seen := st.ids[env.EventID]; seen && seq != env.StreamSeq {
		d.dropped++
		if env.StreamSeq > st.hw {
			st.hw = env.StreamSeq
		}
		return false, nil
	}
	if env.StreamSeq <= st.hw {
		return false, fmt.Errorf("%w: stream %q seq %d <= high-water %d under unrecorded key %q",
			ErrSequenceConflict, env.StreamID, env.StreamSeq, st.hw, key)
	}
	d.recordLocked(st, key, env)
	return true, nil
}

func validDelivery(env *Envelope) error {
	switch {
	case env == nil:
		return fmt.Errorf("%w: nil envelope", ErrInvalidDelivery)
	case env.EventID == "":
		return fmt.Errorf("%w: empty event id", ErrInvalidDelivery)
	case env.StreamID == "":
		return fmt.Errorf("%w: empty stream id", ErrInvalidDelivery)
	case env.Producer == "":
		return fmt.Errorf("%w: empty producer", ErrInvalidDelivery)
	case env.StreamSeq == 0:
		return fmt.Errorf("%w: stream seq 0", ErrInvalidDelivery)
	}
	return nil
}

func (d *Deliverer) stateLocked(stream StreamID) *streamState {
	st, ok := d.streams[stream]
	if !ok {
		st = newStreamState(0)
		d.streams[stream] = st
	}
	return st
}

// recordLocked remembers key and env.EventID, evicting the oldest of each
// beyond the window, and raises the high-water to env.StreamSeq.
func (d *Deliverer) recordLocked(st *streamState, key string, env *Envelope) {
	st.keys[key] = struct{}{}
	st.keyOrder = append(st.keyOrder, key)
	if len(st.keyOrder) > d.window {
		delete(st.keys, st.keyOrder[0])
		st.keyOrder = st.keyOrder[1:]
	}
	if _, seen := st.ids[env.EventID]; !seen {
		st.idOrder = append(st.idOrder, env.EventID)
	}
	st.ids[env.EventID] = env.StreamSeq
	if len(st.idOrder) > d.window {
		delete(st.ids, st.idOrder[0])
		st.idOrder = st.idOrder[1:]
	}
	st.hw = env.StreamSeq
}

// Restore sets stream's high-water from a consumer cursor. The restored
// stream remembers no keys, so any seq <= hw is then a conflict. A value
// below the current high-water is ErrSequenceConflict; an empty stream is
// ErrInvalidDelivery.
func (d *Deliverer) Restore(stream StreamID, hw StreamSeq) error {
	if stream == "" {
		return fmt.Errorf("%w: empty stream id", ErrInvalidDelivery)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.streams[stream]; ok && hw < st.hw {
		return fmt.Errorf("%w: restore stream %q to %d below high-water %d",
			ErrSequenceConflict, stream, hw, st.hw)
	}
	d.streams[stream] = newStreamState(hw)
	return nil
}

// HighWater returns the highest sequence seen on stream (0 when unknown).
func (d *Deliverer) HighWater(stream StreamID) StreamSeq {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.streams[stream]; ok {
		return st.hw
	}
	return 0
}

// Dropped returns how many envelopes Deliver dropped as repeats.
func (d *Deliverer) Dropped() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}
