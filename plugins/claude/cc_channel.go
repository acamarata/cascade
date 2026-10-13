// Package claude (this file): CCChannel, the R-21.158/R-21.199 CC channel
// push adapter.
//
// Purpose: probe the channel capability ({supported, unsupported,
// indeterminate}, indeterminate resolving to unsupported), and ONCE at
// startup authenticate the peer's handshake frame and negotiate the pinned
// channel revision (min=1, max=1, R-14.308) through pkg/provider; then
// deliver envelopes in the captured CC channel notification shape, ordered
// (one delivery at a time) and de-duplicated by envelope id.
//
// Inputs: an unexported probe and transport, fixed by NewCCChannel to the
// production defaults; only this package's tests inject others
// (newCCChannel). Nothing outside the package can make the probe report
// supported.
// Outputs: Start's deferral reason; Deliver's typed errors — the exact
// provider.ErrHarnessIncompatible for an out-of-range revision, a
// ErrChannelUnsupported / ErrWindowsUnsupported deferral, ErrChannelRejected
// for a transport failure, a KindCanceled error on cancellation.
//
// Constraints: the production probe is indeterminate (ruling D-S73T1-1:
// the harness gives a server no observable proof that a pushed channel
// notification reaches a -p-driven session; testdata/cc-channel/README.md),
// so the push path ships flag-off under the Art.6 deferral. Windows is
// tier-2: Start refuses with ErrWindowsUnsupported before probing.
//
// SPORT: plugins/claude/mcp-push-channel (ADD) — P1-E37-W8-S73-T1.
package claude

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	goruntime "runtime"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProbeStatus is the capability probe's typed enum (R-21.199).
type ProbeStatus int

// The three probe outcomes. Only ProbeSupported enables the push path.
const (
	ProbeIndeterminate ProbeStatus = iota
	ProbeUnsupported
	ProbeSupported
)

// Resolve maps ProbeIndeterminate (and any unknown value) to
// ProbeUnsupported: fail closed, never "try anyway".
func (s ProbeStatus) Resolve() ProbeStatus {
	if s == ProbeSupported {
		return ProbeSupported
	}
	return ProbeUnsupported
}

// channelRevision1 is the one channel revision this adapter speaks: the
// notifications/claude/channel {content, meta} shape captured in
// testdata/cc-channel/addressed-inbox-delivery.json.
const channelRevision1 provider.ProtocolVersion = "1"

// channelRange is the declared supported range (R-14.308: min=1, max=1).
var channelRange = provider.ProtocolRange{Min: channelRevision1, Max: channelRevision1}

// mcpRevisionToChannel maps the MCP protocolVersion a harness sends in its
// initialize request to the channel revision it carries. The one row is the
// captured exchange (testdata/cc-channel/protocol-negotiation.json); any
// other value maps to an out-of-range revision so a harness upgrade is
// refused at runtime instead of being mis-parsed.
var mcpRevisionToChannel = map[string]provider.ProtocolVersion{"2025-11-25": channelRevision1}

// channelMethod is the captured server-to-client notification method.
const channelMethod = "notifications/claude/channel"

// maxRememberedIDs bounds the de-duplication set.
const maxRememberedIDs = 4096

var (
	// ErrChannelUnsupported is the Art.6 deferral: the probe did not report
	// supported, so the push path stays off.
	ErrChannelUnsupported = cascade.New(cascade.KindUnsupported, "claude: CC channel push is unsupported for this session (deferral D-S73T1-1: no server-observable delivery signal)")
	// ErrWindowsUnsupported is the asserted tier-2 platform refusal (Art.5).
	ErrWindowsUnsupported = cascade.New(cascade.KindUnsupported, "claude: active-session channel push is not supported on Windows (tier-2: no daemon session service)")
	// ErrChannelNotStarted refuses Deliver before Start.
	ErrChannelNotStarted = cascade.New(cascade.KindUnavailable, "claude: CC channel used before Start")
	// ErrChannelRejected reports a transport failure on delivery.
	ErrChannelRejected = cascade.New(cascade.KindUnavailable, "claude: CC channel rejected the delivery")
	// ErrHandshakeInvalid refuses a handshake payload that is empty, over
	// 16 KB, not JSON, or has no protocolVersion.
	ErrHandshakeInvalid = cascade.New(cascade.KindInvalidInput, "claude: CC channel handshake is not a valid initialize request")
)

// probeResult is one probe's outcome and the peer's handshake frame.
type probeResult struct {
	status    ProbeStatus
	handshake provider.ControlFrame
}

type (
	probeFunc     func(ctx context.Context, token provider.SessionToken) (probeResult, error)
	transportFunc func(ctx context.Context, target string, frame provider.ControlFrame) error
)

// indeterminateProbe is the production probe (see the file's Constraints).
func indeterminateProbe(context.Context, provider.SessionToken) (probeResult, error) {
	return probeResult{status: ProbeIndeterminate}, nil
}

// refusingTransport is the production transport. It is unreachable while
// the probe is indeterminate; it refuses rather than pretending to send.
func refusingTransport(context.Context, string, provider.ControlFrame) error {
	return ErrChannelUnsupported
}

// CCChannel is the stateful channel adapter. Construct with NewCCChannel.
type CCChannel struct {
	goos      string
	probe     probeFunc
	transport transportFunc

	mu       sync.Mutex
	started  bool
	deferral error
	token    provider.SessionToken
	seen     map[string]struct{}
	order    []string
}

// NewCCChannel returns the production channel for this platform.
func NewCCChannel() *CCChannel {
	return newCCChannel(goruntime.GOOS, indeterminateProbe, refusingTransport)
}

func newCCChannel(goos string, probe probeFunc, transport transportFunc) *CCChannel {
	return &CCChannel{goos: goos, probe: probe, transport: transport, seen: make(map[string]struct{})}
}

// Start probes once, authenticates the handshake frame and negotiates the
// channel revision. A non-nil return is the deferral reason Deliver keeps
// returning; it is the designed Art.6 outcome, not a daemon failure.
func (c *CCChannel) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = true
	c.deferral = c.negotiate(ctx)
	return c.deferral
}

// negotiate is Start's body, run under c.mu.
func (c *CCChannel) negotiate(ctx context.Context) error {
	if c.goos == "windows" {
		return ErrWindowsUnsupported
	}
	var token provider.SessionToken
	if _, err := cryptorand.Read(token[:]); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "claude: mint CC channel session token")
	}
	res, err := c.probe(ctx, token)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "claude: CC channel probe")
	}
	if res.status.Resolve() != ProbeSupported {
		return ErrChannelUnsupported
	}
	payload, err := provider.AuthenticateFrame(token, res.handshake)
	if err != nil {
		return err
	}
	peer, err := decodeHandshake(payload)
	if err != nil {
		return err
	}
	if _, err := provider.NegotiateProtocol(channelRange, peer); err != nil {
		return err
	}
	c.token = token
	return nil
}

// decodeHandshake reads the harness's MCP initialize params and returns the
// channel revision range they carry. It never panics (FuzzCCChannelDecode).
func decodeHandshake(payload []byte) (provider.ProtocolRange, error) {
	if len(payload) == 0 || len(payload) > maxEnvelopeBytes {
		return provider.ProtocolRange{}, ErrHandshakeInvalid
	}
	var hs struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(payload, &hs); err != nil {
		return provider.ProtocolRange{}, cascade.Wrapf(cascade.KindInvalidInput, ErrHandshakeInvalid, "claude: decode handshake: %v", err)
	}
	if hs.ProtocolVersion == "" {
		return provider.ProtocolRange{}, ErrHandshakeInvalid
	}
	rev, ok := mcpRevisionToChannel[hs.ProtocolVersion]
	if !ok {
		rev = "0"
	}
	return provider.ProtocolRange{Min: rev, Max: rev}, nil
}

// Deliver sends env to target. Deliveries are serialised under c.mu, which
// orders them and makes the de-duplication check-and-mark atomic: a
// redelivered id is a no-op returning nil, never a second send.
func (c *CCChannel) Deliver(ctx context.Context, target string, env Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrChannelNotStarted
	}
	if c.deferral != nil {
		return c.deferral
	}
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "claude: CC channel delivery canceled")
	}
	key := target + "\x00" + env.ID
	if _, dup := c.seen[key]; dup {
		return nil
	}
	payload, err := encodeChannelNotification(env)
	if err != nil {
		return err
	}
	if err := c.transport(ctx, target, provider.ControlFrame{Token: c.token, Payload: payload}); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrChannelRejected, "claude: deliver %q to %q: %v", env.ID, target, err)
	}
	c.remember(key)
	return nil
}

// remember records key (target and envelope id), evicting the oldest past maxRememberedIDs.
func (c *CCChannel) remember(key string) {
	c.seen[key] = struct{}{}
	c.order = append(c.order, key)
	if len(c.order) > maxRememberedIDs {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
}

// channelNotification is the captured notifications/claude/channel shape:
// {jsonrpc, method, params: {content, meta}} with string-valued meta.
type channelNotification struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  channelParams `json:"params"`
}

type channelParams struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

// encodeChannelNotification wraps env's rendered data block in the
// captured notification shape, bounded at 16 KB.
func encodeChannelNotification(env Envelope) ([]byte, error) {
	return encodeBounded(channelNotification{
		JSONRPC: "2.0",
		Method:  channelMethod,
		Params: channelParams{
			Content: RenderInboxBlock([]InboxRecord{{Envelope: env, Provenance: ProvenanceUntrusted}}),
			Meta: map[string]string{
				"source": "cascade-inbox", "event_id": env.ID,
				"data_class": env.DataClass, "provenance": ProvenanceUntrusted,
			},
		},
	})
}
