// Purpose: audit.gate_append's handler: decode, validate, bound and append
// one local elevation gate row in the daemon.
// Inputs: the row's raw params, the daemon's gate log and clock.
// Outputs: gateAppendHandler, acknowledging the committed sequence number.
// Constraints: exactly one gateRow (no unknown fields, no trailing data, at
// most gateRowMaxBytes); every field inside the row vocabulary; at most
// gateAppendPerSecond rows a second (burst gateAppendBurst). Registered in
// wire_gate_audit.go.
// SPORT: cmd/cascade audit.gate_append handler (P1-SEC-08).

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gateAppendPerSecond and gateAppendBurst shape audit.gate_append as a
// token bucket: a gate call writes at most four rows, so 10 a second with a
// burst of 20 serves any interactive use, and a flood is smoothed instead of
// landing 600 rows at once. A same-uid process can still starve the CLI by
// flooding steadily; it could equally kill the daemon, so this is not a new
// trust boundary (docs/elevation.md, threat model).
const (
	gateAppendPerSecond = 10
	gateAppendBurst     = 20
)

// gateAppendBucket is the token bucket. It starts full; a clock that moves
// backwards refills nothing.
type gateAppendBucket struct {
	mu      sync.Mutex
	started bool
	tokens  float64
	last    time.Time
}

func (b *gateAppendBucket) admit(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		b.started, b.tokens, b.last = true, gateAppendBurst, now
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(gateAppendBurst, b.tokens+elapsed.Seconds()*gateAppendPerSecond)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gateAppendHandler decodes, validates and appends one gate row.
func gateAppendHandler(sink gateSink, clock runtime.Clock) func(context.Context, json.RawMessage) (any, error) {
	bucket := &gateAppendBucket{}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if len(raw) > gateRowMaxBytes {
			return nil, cascade.New(cascade.KindInvalidInput, "audit.gate_append: the row is too large")
		}
		var row gateRow
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&row); err != nil || dec.More() {
			return nil, cascade.New(cascade.KindInvalidInput, "audit.gate_append: params are not one gate row")
		}
		if err := row.validate(); err != nil {
			return nil, err
		}
		if !bucket.admit(clock.Now()) {
			return nil, cascade.New(cascade.KindQuotaExhausted, "audit.gate_append: too many rows; retry shortly")
		}
		seq, err := sink.AppendGateRow(ctx, row)
		if err != nil {
			return nil, err
		}
		return gateAppendAck{Seq: seq}, nil
	}
}
