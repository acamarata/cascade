// Purpose: the lane-outcome decorator Resolver.build wraps around every
//
//	driver it constructs, and the per-call observation slot plus the
//	Transport wrapper that fills it. Together they turn a real provider
//	call into one lane-state write: the wrapped Transport records the
//	status and Retry-After of the HTTP response a call actually received,
//	and once the call returns the decorator classifies the DRIVER's own
//	returned cascade.Kind against that observation (lane_outcome_classify.go)
//	and writes the lane (lane_outcome_record.go).
//
// Inputs: the real provider.ModelProvider the driver switch built, the
//
//	lane it serves, and the Transport the driver sends through.
//
// Outputs: a provider.ModelProvider whose five verbs return exactly what
//
//	the wrapped driver returned, and an observing Transport.
//
// Constraints: the observation slot lives in the call's context, so two
//
//	concurrent calls on one driver never share one; it keeps only the
//	LAST response of a call (openai retries 429 internally), and only
//	its status and Retry-After - never a request header, URL or body,
//	because credentials ride the request. A recorder failure never
//	changes a call's result. Capabilities makes no HTTP call, so it is
//	passed through unobserved.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/transport"
)

// retryAfterHeader is the one response header the observation keeps.
const retryAfterHeader = "Retry-After"

// observation is one call's slot. The zero value means "no response seen".
type observation struct {
	mu         sync.Mutex
	status     int
	retryAfter []string
}

// observationKey is the context key the slot rides under.
type observationKey struct{}

// withObservation returns ctx carrying a fresh slot, and the slot.
func withObservation(ctx context.Context) (context.Context, *observation) {
	slot := &observation{}
	return context.WithValue(ctx, observationKey{}, slot), slot
}

// observationFrom returns the slot ctx carries, or nil.
func observationFrom(ctx context.Context) *observation {
	slot, _ := ctx.Value(observationKey{}).(*observation)
	return slot
}

// record keeps status and the Retry-After values of headers (matched
// case-insensitively), replacing whatever an earlier response left.
func (o *observation) record(status int, headers map[string][]string) {
	var kept []string
	for name, values := range headers {
		if strings.EqualFold(name, retryAfterHeader) {
			kept = append(kept, values...)
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status, o.retryAfter = status, kept
}

// snapshot returns the last response seen: its status (0 when none) and
// a header map holding only Retry-After.
func (o *observation) snapshot() (int, map[string][]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.retryAfter) == 0 {
		return o.status, nil
	}
	return o.status, map[string][]string{retryAfterHeader: append([]string(nil), o.retryAfter...)}
}

// observingTransport wraps a transport.Transport so each response a call
// receives lands in that call's observation slot. A send that fails
// returns no response and records nothing. Request arguments are passed
// straight through and never inspected.
type observingTransport struct{ inner transport.Transport }

var _ transport.Transport = observingTransport{}

// Send forwards to the wrapped Transport and records the response, if any.
func (t observingTransport) Send(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, map[string][]string, io.ReadCloser, error) {
	status, respHeaders, respBody, err := t.inner.Send(ctx, method, url, headers, body)
	if err == nil {
		if slot := observationFrom(ctx); slot != nil {
			slot.record(status, respHeaders)
		}
	}
	return status, respHeaders, respBody, err
}

// laneOutcomeProvider is the decorator: it forwards every verb to inner
// and records the outcome of the HTTP-backed ones on the lane.
type laneOutcomeProvider struct {
	inner provider.ModelProvider
	rec   *laneRecorder
}

var _ provider.ModelProvider = (*laneOutcomeProvider)(nil)

// Unwrap returns the real driver this decorator wraps.
func (p *laneOutcomeProvider) Unwrap() provider.ModelProvider { return p.inner }

// Chat forwards to the driver and records the outcome.
func (p *laneOutcomeProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	ctx, slot := withObservation(ctx)
	resp, err := p.inner.Chat(ctx, req)
	p.rec.record(ctx, slot, err)
	return resp, err
}

// Embed forwards to the driver and records the outcome.
func (p *laneOutcomeProvider) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	ctx, slot := withObservation(ctx)
	resp, err := p.inner.Embed(ctx, req)
	p.rec.record(ctx, slot, err)
	return resp, err
}

// Count forwards to the driver and records the outcome.
func (p *laneOutcomeProvider) Count(ctx context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	ctx, slot := withObservation(ctx)
	resp, err := p.inner.Count(ctx, req)
	p.rec.record(ctx, slot, err)
	return resp, err
}

// Stream forwards to the driver and records the outcome at its terminal
// error (or at a clean end).
func (p *laneOutcomeProvider) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) error {
	ctx, slot := withObservation(ctx)
	err := p.inner.Stream(ctx, req, sink)
	p.rec.record(ctx, slot, err)
	return err
}

// Capabilities forwards unobserved: it describes a lane and sends nothing.
func (p *laneOutcomeProvider) Capabilities(ctx context.Context, lane string) (provider.Capabilities, error) {
	return p.inner.Capabilities(ctx, lane)
}
