// Purpose: the heartbeat protocol: the controller-side decode/verify/
//
//	apply path that keeps a S-36.T1 device record's LastSeen current, the
//	node-side scheduler that sends heartbeats on a deterministic clock,
//	and the JSON-RPC "node.heartbeat" handler that wires the controller
//	side to a production caller.
//
// Inputs: HeartbeatFrame wire bytes from an enrolled-but-untrusted-per-
//
//	frame peer (heartbeat_sign.go's VerifyHeartbeatFrame decides whether
//	to trust any given frame).
//
// Outputs: an updated DeviceRecord.LastSeen and a derived Liveness, or a
//
//	typed fail-closed error.
//
// Constraints: A HEARTBEAT IS AN AUTHENTICATED CHANNEL (this ticket's own
//
//	security-boundary text) — an unenrolled, unknown, expired, revoked or
//	unparseable node identity is refused, never defaulted to "unknown
//	means allow". Scheduling runs on the injected Clock/Ticker only
//	(Art.7.3 — no sleeps as synchronization). Controller-unreachable is a
//	typed error path with retry, never a crash.
//
// SPORT: internal/nodes ProcessHeartbeat/ADDED,
//
//	RegisterHeartbeatHandler/ADDED, RunHeartbeatLoop/ADDED
//	(P1-E17-W4-S36-T2).

package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxHeartbeatPayloadBytes bounds the decoder against a memory-exhaustion
// attack, mirroring enroll.go's maxEnrollPayloadBytes.
const maxHeartbeatPayloadBytes = 64 * 1024

// DefaultHeartbeatInterval is the node-side scheduler's default tick
// period when HeartbeatLoopOptions.Interval is left at its zero value.
const DefaultHeartbeatInterval = 30 * time.Second

// DecodeHeartbeatFrame fail-closed-decodes raw untrusted bytes into a
// HeartbeatFrame. It never panics on any input and never returns a
// partially-valid frame: every required field is checked before the
// frame is handed back (shape only — heartbeat_sign.go's
// VerifyHeartbeatFrame checks the semantics: signature, revocation,
// enrollment id, sequence).
func DecodeHeartbeatFrame(raw []byte) (HeartbeatFrame, error) {
	if len(raw) == 0 {
		return HeartbeatFrame{}, cascade.New(cascade.KindInvalidInput, "nodes: heartbeat frame is empty")
	}
	if len(raw) > maxHeartbeatPayloadBytes {
		return HeartbeatFrame{}, cascade.Newf(cascade.KindInvalidInput,
			"nodes: heartbeat frame exceeds %d bytes", maxHeartbeatPayloadBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f HeartbeatFrame
	if err := dec.Decode(&f); err != nil {
		return HeartbeatFrame{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: heartbeat frame is not valid JSON")
	}
	if dec.More() {
		return HeartbeatFrame{}, cascade.New(cascade.KindInvalidInput, "nodes: heartbeat frame has trailing data after the JSON object")
	}
	if f.NodeID == "" || f.EnrollmentID == "" {
		return HeartbeatFrame{}, cascade.New(cascade.KindInvalidInput, "nodes: heartbeat frame missing node_id or enrollment_id")
	}
	if f.SignatureB64 == "" {
		return HeartbeatFrame{}, cascade.New(cascade.KindInvalidInput, "nodes: heartbeat frame missing signature_b64")
	}
	if err := ValidateCapabilityReport(f.Report); err != nil {
		return HeartbeatFrame{}, err
	}
	return f, nil
}

// HeartbeatDeps carries every collaborator ProcessHeartbeat needs.
type HeartbeatDeps struct {
	Records   *RecordStore
	Sequences *SequenceStore
	Clock     Clock
	// Timeout is the liveness freshness window (liveness.go). Zero means
	// DefaultHeartbeatTimeout.
	Timeout time.Duration
}

// HeartbeatResult is ProcessHeartbeat's success outcome, echoed back to
// the node as the RPC result and read directly by doctor.go's health
// check and tests.
type HeartbeatResult struct {
	NodeID   string    `json:"node_id"`
	LastSeen time.Time `json:"last_seen"`
	Liveness Liveness  `json:"liveness"`
}

// ProcessHeartbeat runs the full controller-side heartbeat operation: an
// unenrolled node id is refused (cascade.ErrNotFound's KindNotFound from
// RecordStore.Get, unknown-means-refuse, never allow); a known node's
// frame is verified (heartbeat_sign.go: spoofed signer, revoked key,
// enrollment mismatch, replayed sequence all refuse); a verified frame
// advances the sequence store and updates the device record's LastSeen
// via RecordStore's own package-private put (records.go, same package,
// no new field added). Every failure mode is a typed fail-closed error;
// there is no path that returns a zero-value success for an unverifiable
// frame.
func ProcessHeartbeat(_ context.Context, deps HeartbeatDeps, f HeartbeatFrame) (HeartbeatResult, error) {
	rec, err := deps.Records.Get(f.NodeID)
	if err != nil {
		// Unknown node id: refused, never treated as "unknown means
		// allow". RecordStore.Get already returns a typed KindNotFound.
		return HeartbeatResult{}, err
	}
	lastSeq := deps.Sequences.Last(f.NodeID)
	if err := VerifyHeartbeatFrame(f, rec, lastSeq); err != nil {
		return HeartbeatResult{}, err
	}
	deps.Sequences.Advance(f.NodeID, f.Sequence)

	now := deps.Clock.Now()
	rec.LastSeen = now
	if err := deps.Records.put(rec); err != nil {
		return HeartbeatResult{}, err
	}

	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = DefaultHeartbeatTimeout
	}
	return HeartbeatResult{
		NodeID:   rec.NodeID,
		LastSeen: rec.LastSeen,
		Liveness: ComputeLiveness(rec, now, timeout),
	}, nil
}

// RegisterHeartbeatHandler mounts the "node.heartbeat" JSON-RPC method on
// registry, wiring ProcessHeartbeat to the daemon's real dispatch path —
// this IS the production caller for ProcessHeartbeat and
// DecodeHeartbeatFrame, mirroring enroll.go's RegisterHandlers exactly.
func RegisterHeartbeatHandler(registry *rpc.Registry, deps HeartbeatDeps) {
	registry.Register("node.heartbeat", func(ctx context.Context, params json.RawMessage) (any, error) {
		frame, err := DecodeHeartbeatFrame(params)
		if err != nil {
			return nil, err
		}
		return ProcessHeartbeat(ctx, deps, frame)
	})
}

// HeartbeatSender sends one signed heartbeat frame to the controller and
// reports whether it was accepted. Production implementations POST the
// frame to the controller's node.heartbeat RPC method; tests inject a
// fake. Controller-unreachable is this function's typed error path (never
// a crash) — RunHeartbeatLoop retries on the next scheduled tick rather
// than treating a single failed send as fatal.
type HeartbeatSender func(ctx context.Context, f HeartbeatFrame) error

// BuildHeartbeatFrame assembles and signs the next heartbeat frame for
// nodeID, using keystore to sign (the private key never leaves custody,
// per keystore.go's R-21.220 contract) and seq as this frame's sequence
// number (the caller is responsible for strict monotonicity — see
// HeartbeatLoopOptions.NextSequence).
func BuildHeartbeatFrame(ctx context.Context, keystore *NodeKeystore, nodeID, enrollmentID string, seq uint64, report CapabilityReport) (HeartbeatFrame, error) {
	f := HeartbeatFrame{NodeID: nodeID, EnrollmentID: enrollmentID, Sequence: seq, Report: report}
	payload, err := f.signingPayload()
	if err != nil {
		return HeartbeatFrame{}, err
	}
	sig, err := keystore.Sign(ctx, nodeID, payload)
	if err != nil {
		return HeartbeatFrame{}, err
	}
	f.SignatureB64 = base64.StdEncoding.EncodeToString(sig)
	return f, nil
}

// HeartbeatLoopOptions configures RunHeartbeatLoop.
type HeartbeatLoopOptions struct {
	// Ticker paces the loop (Art.7.3: no real sleeps in a test). A nil
	// Ticker is a programmer error the caller must not make; production
	// wires a real one (cmd/cascade/node_serve.go).
	Ticker Ticker
	// NextSequence returns the next strictly-increasing sequence number
	// to send. Injected so tests control it deterministically and
	// production can back it with a monotonic in-memory counter.
	NextSequence func() uint64
	// Send delivers one built frame to the controller.
	Send HeartbeatSender
	// BuildReport returns the current capability report to send on each
	// tick (hardware figures can legitimately change between ticks, e.g.
	// after a governor recalibration).
	BuildReport  func() CapabilityReport
	Keystore     *NodeKeystore
	NodeID       string
	EnrollmentID string
	// OnError is called with any Send/build error on a tick — a typed,
	// non-fatal retry-next-tick path, never a crash. Nil is a valid
	// no-op.
	OnError func(error)
}

// RunHeartbeatLoop sends one heartbeat frame per tick from opts.Ticker
// until ctx is canceled. Every send failure (build error, sign error, or
// a controller-unreachable HeartbeatSender error) is reported to
// opts.OnError and the loop continues to the next tick — a single failed
// heartbeat never terminates the node agent.
func RunHeartbeatLoop(ctx context.Context, opts HeartbeatLoopOptions) {
	defer opts.Ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-opts.Ticker.C():
			report := opts.BuildReport()
			f, err := BuildHeartbeatFrame(ctx, opts.Keystore, opts.NodeID, opts.EnrollmentID, opts.NextSequence(), report)
			if err != nil {
				reportErr(opts.OnError, err)
				continue
			}
			if err := opts.Send(ctx, f); err != nil {
				reportErr(opts.OnError, cascade.Wrap(cascade.KindUnavailable, err, "nodes: heartbeat send failed, will retry on the next tick"))
			}
		}
	}
}

func reportErr(onError func(error), err error) {
	if onError != nil {
		onError(err)
	}
}

// Ticker abstracts periodic notification so RunHeartbeatLoop never blocks
// on a real sleep in tests, duck-typed against internal/runtime.Ticker
// (mirroring records.go's Clock duck-type: this package must not import
// internal/runtime for a two-method interface it does not otherwise
// need).
type Ticker interface {
	C() <-chan struct{}
	Stop()
}

// systemTicker is the production Ticker, backed by a real time.Ticker,
// mirroring internal/runtime's own systemTicker (metrics_emitter.go)
// exactly: this package cannot import internal/runtime for its Ticker
// concrete type without importing the whole runtime package for one
// helper, so the same small adapter is duplicated here rather than
// exported cross-package.
type systemTicker struct {
	t *time.Ticker
	c chan struct{}
}

// NewSystemTicker returns the production Ticker, firing every d (d must
// be positive). Only production entrypoints should call this; tests
// inject a fake Ticker instead (heartbeat_test.go's fakeTicker).
func NewSystemTicker(d time.Duration) Ticker {
	st := &systemTicker{t: time.NewTicker(d), c: make(chan struct{}, 1)}
	go func() {
		for range st.t.C {
			select {
			case st.c <- struct{}{}:
			default:
			}
		}
	}()
	return st
}

func (s *systemTicker) C() <-chan struct{} { return s.c }
func (s *systemTicker) Stop()              { s.t.Stop() }
