package bgem3

// Purpose: the client half of the BGE-M3 sidecar seam — the code that
//   dials a sidecar, sends one framed embed request, decodes the answer,
//   and either returns vectors it has fully verified or refuses with a
//   pkg/cascade taxonomy error.
// Inputs: a Config naming how to reach the sidecar and which embedding
//   space it is configured to produce, plus a batch of texts.
// Outputs: []provider.EmbedOutput satisfying pkg/provider.Embedder's
//   contract, or a taxonomy error.
// Constraints: THE CLIENT NEVER INVENTS A VECTOR. A missing, slow, wedged,
//   mismatched, or wrong-answering sidecar produces an error, never a
//   zero, truncated, or padded embedding — a fabricated vector poisons an
//   index silently and is unrecoverable without a full reindex (v1's
//   MockEmbedModel lesson, Art.1). Not wired: nothing registers this
//   client as an active embedder lane in P1; the sidecar it speaks to is
//   a separate post-P1 artifact (SPEC.md §Status).
// SPORT: providers/embeddings/bgem3 (ADD).

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DialFunc opens one bidirectional byte stream to the sidecar.
//
// It yields an io.ReadWriteCloser rather than a net.Conn deliberately: the
// protocol needs an ordered, reliable duplex stream and nothing more, so
// the transport stays the deployment's decision — a unix socket, a
// loopback connection, or a pipe pair to a child process all satisfy it,
// and this package needs no knowledge of where a sidecar is installed.
type DialFunc func(ctx context.Context) (io.ReadWriteCloser, error)

// Config describes one sidecar seam: how to reach it, what embedding
// space it is configured to produce, and how long a call may take.
type Config struct {
	// Dial opens a connection to the sidecar. Required.
	Dial DialFunc
	// Endpoint names what Dial reaches, for error messages only. It never
	// affects dialing.
	Endpoint string
	// Model is the embedding space this seam is configured for. A sidecar
	// that answers with any other model id or vector width is refused,
	// never accepted with a warning: two embedding spaces mixed in one
	// namespace are undetectable afterwards (pkg/provider.EmbedModel).
	Model provider.EmbedModel
	// Timeout bounds one Embed call end to end. Zero means the call is
	// bounded only by the caller's context.
	Timeout time.Duration
}

// Client speaks the BGE-M3 sidecar protocol (SPEC.md). It satisfies the
// shape of pkg/provider.Embedder.
//
// One call, one connection: Embed dials, writes exactly one request frame,
// reads exactly one response frame, and closes. There is no pooling and no
// multiplexing, which is what makes a canceled or timed-out call harmless
// — an abandoned connection is closed, never returned to a pool holding
// half a response that would desynchronize the next caller's read.
type Client struct {
	dial     DialFunc
	endpoint string
	model    provider.EmbedModel
	timeout  time.Duration
}

// New builds a Client from cfg. It refuses a configuration that could not
// produce verifiable vectors: no dialer, or an unset model identity (an
// empty id or a non-positive width), because without a model identity to
// compare against there is nothing to check a sidecar's answer for.
func New(cfg Config) (*Client, error) {
	if cfg.Dial == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "bgem3: Config.Dial is required")
	}
	if cfg.Model.ID == "" || cfg.Model.Dimensions <= 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"bgem3: Config.Model must name a model and a positive width, got %q/%d",
			cfg.Model.ID, cfg.Model.Dimensions)
	}
	if cfg.Timeout < 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"bgem3: Config.Timeout must not be negative, got %s", cfg.Timeout)
	}
	return &Client{
		dial:     cfg.Dial,
		endpoint: cfg.Endpoint,
		model:    cfg.Model,
		timeout:  cfg.Timeout,
	}, nil
}

// Client implements pkg/provider.Embedder. The assertion is here so a
// change to either side breaks the build rather than the seam.
var _ provider.Embedder = (*Client)(nil)

// Model returns the embedding space this client is configured for. It is
// constant for the client's lifetime and performs no I/O.
func (c *Client) Model() provider.EmbedModel {
	return c.model
}

// Embed sends one batch to the sidecar and returns one verified vector per
// input, in input order.
//
// An empty batch is answered locally: no connection is opened, because
// there is nothing to ask about. Every other outcome either returns a
// complete, verified batch or a taxonomy error — there is no partial
// success, and no substituted vector under any failure.
func (c *Client) Embed(ctx context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	if len(inputs) == 0 {
		return []provider.EmbedOutput{}, nil
	}
	texts := make([]string, len(inputs))
	for i, in := range inputs {
		texts[i] = in.Text
	}
	frame, err := encodeRequestFrame(c.model.ID, c.model.Dimensions, texts)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	resp, err := c.exchange(callCtx, frame)
	if err != nil {
		return nil, err
	}
	return c.verify(inputs, resp)
}

// withTimeout derives the deadline one call runs under. A zero
// Config.Timeout leaves the caller's context untouched, so a caller that
// wants to manage its own deadline is not overridden.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// exchange performs the one stream-bound step: dial, write the request
// frame, read the response frame, close.
//
// Cancellation and the deadline are both enforced by CLOSING the stream,
// via context.AfterFunc registered for the duration of the call and
// stopped before return. A blocking Read or Write on a closed stream
// fails immediately, so a wedged sidecar cannot hold the caller past its
// deadline; the AfterFunc leaves no goroutine behind either way. Closing
// is used rather than a transport-specific SetDeadline so the rule holds
// for every stream a DialFunc can return, not only for net.Conn.
func (c *Client) exchange(ctx context.Context, frame []byte) (*wireResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, c.classify(ctx, err, "connecting to the sidecar")
	}
	defer func() { _ = conn.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if _, err := conn.Write(frame); err != nil {
		return nil, c.classify(ctx, err, "sending the embed request")
	}
	resp, err := decodeResponseFrame(conn)
	if err != nil {
		return nil, c.classify(ctx, err, "reading the embed response")
	}
	return resp, nil
}

// classify maps a connection-level or framing failure to the taxonomy. A
// canceled or expired context wins over whatever I/O error the closed
// connection produced, because that error is a consequence of the
// cancellation rather than an independent condition. A framing failure is
// KindIntegrity: the transport worked and the peer answered, but what it
// said does not conform to the protocol.
func (c *Client) classify(ctx context.Context, err error, stage string) error {
	where := stage
	if c.endpoint != "" {
		where = stage + " at " + c.endpoint
	}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return cascade.Wrap(cascade.KindCanceled, err, "bgem3: canceled while "+where)
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return cascade.Wrap(cascade.KindTimeout, err, "bgem3: timed out while "+where)
	case isFramingError(err):
		return cascade.Wrap(cascade.KindIntegrity, err, "bgem3: malformed response while "+where)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return cascade.Wrap(cascade.KindTimeout, err, "bgem3: timed out while "+where)
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "bgem3: sidecar unreachable while "+where)
}

// isFramingError reports whether err came from the protocol decoder rather
// than from the connection underneath it.
func isFramingError(err error) bool {
	for _, sentinel := range []error{
		errFrameHeaderShort, errFramePayloadShort, errFrameEmpty, errFrameTooLarge,
		errPayloadNotJSON,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// verify turns a decoded response into outputs, or refuses it. Nothing
// short of a fully conforming answer produces vectors.
func (c *Client) verify(inputs []provider.EmbedInput, resp *wireResponse) ([]provider.EmbedOutput, error) {
	if err := checkProtocolVersion(resp.ProtocolVersion); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		if len(resp.Vectors) != 0 {
			return nil, cascade.New(cascade.KindIntegrity,
				"bgem3: sidecar returned both vectors and an error")
		}
		return nil, cascade.Newf(kindForSidecarCode(resp.Error.Code),
			"bgem3: sidecar refused the batch (%s): %s", resp.Error.Code, resp.Error.Message)
	}
	if err := c.checkIdentity(resp); err != nil {
		return nil, err
	}
	outputs := make([]provider.EmbedOutput, 0, len(resp.Vectors))
	for i, vec := range resp.Vectors {
		if err := checkVector(i, vec, c.model.Dimensions); err != nil {
			return nil, err
		}
		outputs = append(outputs, provider.EmbedOutput{Vector: vec, Model: c.model})
	}
	if !c.model.ValidBatch(inputs, outputs) {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"bgem3: sidecar returned %d vectors for %d inputs", len(outputs), len(inputs))
	}
	return outputs, nil
}

// checkIdentity refuses a response whose model identity is not the one
// this client was configured for. A differing id or width means the
// sidecar is producing a different embedding space, which no downstream
// similarity check could ever detect.
func (c *Client) checkIdentity(resp *wireResponse) error {
	reported := provider.EmbedModel{ID: resp.Model, Dimensions: resp.Dimensions}
	if !reported.Equal(c.model) {
		return cascade.Newf(cascade.KindIntegrity,
			"bgem3: sidecar reported model %q/%d, this client is configured for %q/%d",
			resp.Model, resp.Dimensions, c.model.ID, c.model.Dimensions)
	}
	return nil
}

// checkVector refuses a vector of the wrong width or carrying a
// non-finite component. The width check is what stops a truncated or
// padded vector from reaching an index; the finite check is the value-level
// half of the same rule — a NaN or infinity poisons every later similarity
// computation it participates in. JSON cannot spell either literal, so the
// finite check guards a peer that encodes an out-of-range value some other
// way, and costs one comparison per component to keep the refusal total.
func checkVector(index int, vec []float32, dimensions int) error {
	if len(vec) != dimensions {
		return cascade.Newf(cascade.KindIntegrity,
			"bgem3: vector %d has %d components, the configured model is %d-dimensional",
			index, len(vec), dimensions)
	}
	for pos, v := range vec {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return cascade.Newf(cascade.KindIntegrity,
				"bgem3: vector %d component %d is not a finite number", index, pos)
		}
	}
	return nil
}
