package plugins

// Purpose: install the host's evidence reader over the daemon client. The
// daemon owns ordering, data classes and redaction; this adapter only decodes.
// Inputs: ticket IDs and daemon wire rows. Outputs: plugin evidence entries.
// Constraints: preserve transport errors and the SDK's per-request identity.
// SPORT: internal/plugins:pbd-evidence-wiring (P1-PBD-02d).

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd"
)

func init() {
	_ = pbd.SetEvidenceReader(newPBDEvidenceReader(client.UnixDialer, 5*time.Second, runtime.NewDefaultPathProvider))
}

type pbdEvidenceReader struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	doer         rpcDoer
}

func newPBDEvidenceReader(dial client.DialFunc, timeout time.Duration, paths pathResolver) *pbdEvidenceReader {
	return &pbdEvidenceReader{dial: dial, timeout: timeout, resolvePaths: paths}
}

func (r *pbdEvidenceReader) rpcClient() (rpcDoer, error) {
	if r.doer != nil {
		return r.doer, nil
	}
	paths, err := r.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "pbd evidence: resolve daemon socket path")
	}
	// New's transport reads the principal environment on each request, including
	// SSE subscriptions; no token or owner identity is cached at init.
	return client.New(paths.SocketPath(), r.dial, r.timeout), nil
}

// QueryAll decodes the daemon's append-ordered rows without projecting fields.
func (r *pbdEvidenceReader) QueryAll(ctx context.Context, ticketID string) ([]pbd.EvidenceEntry, error) {
	rpc, err := r.rpcClient()
	if err != nil {
		return nil, err
	}
	var result struct {
		Ticket string              `json:"ticket"`
		Rows   []pbd.EvidenceEntry `json:"rows"`
	}
	if err := rpc.Do(ctx, "jobs.evidence.list", map[string]string{"ticket": ticketID}, &result); err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// Watch emits each wire row until cancellation, preserving stream and sink errors.
func (r *pbdEvidenceReader) Watch(ctx context.Context, ticketID string, emit func(pbd.EvidenceEntry) error) error {
	if ctx.Err() != nil {
		return nil
	}
	rpc, err := r.rpcClient()
	if err != nil {
		return err
	}
	streamer, ok := rpc.(replySubscriber)
	if !ok {
		return cascade.New(cascade.KindInternal, "pbd evidence: topic subscription unavailable")
	}
	events, closeStream, err := streamer.StreamTopic(ctx, "jobs.evidence", url.Values{"ticket": {ticketID}})
	if err != nil {
		return err
	}
	defer closeStream()
	return forwardPBDEvidence(ctx, events, emit)
}

func forwardPBDEvidence(ctx context.Context, events <-chan client.Event, emit func(pbd.EvidenceEntry) error) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if ctx.Err() != nil {
				return nil
			}
			if !ok {
				return cascade.New(cascade.KindUnavailable, "pbd evidence: daemon stream closed")
			}
			if event.Err != nil {
				return event.Err
			}
			var row pbd.EvidenceEntry
			if err := json.Unmarshal([]byte(event.Data), &row); err != nil {
				return cascade.Wrap(cascade.KindIntegrity, err, "pbd evidence: invalid daemon row")
			}
			if emit == nil {
				return cascade.New(cascade.KindInvalidInput, "pbd evidence: missing row consumer")
			}
			if err := emit(row); err != nil {
				return err
			}
		}
	}
}
