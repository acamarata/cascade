package pbd

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EvidenceEntry carries the evidence row supplied by the host, including its
// data class and redaction state. This plugin makes no policy decisions.
type EvidenceEntry struct {
	JobID              string    `json:"job_id"`
	Seq                int64     `json:"seq"`
	Kind               string    `json:"kind"`
	ProducerCapability string    `json:"producer_capability"`
	AttemptID          string    `json:"attempt_id"`
	CheckpointID       string    `json:"checkpoint_id"`
	TreeHash           string    `json:"tree_hash"`
	AttestorIdentity   string    `json:"attestor_identity,omitempty"`
	Outcome            string    `json:"outcome"`
	ResultHash         string    `json:"result_hash,omitempty"`
	IdempotencyKey     string    `json:"idempotency_key,omitempty"`
	ArtifactRef        string    `json:"artifact_ref,omitempty"`
	StartedAt          time.Time `json:"started_at"`
	EndedAt            time.Time `json:"ended_at"`
	CreatedAt          time.Time `json:"created_at"`
	DataClass          string    `json:"data_class"`
	Redacted           bool      `json:"redacted"`
}

// EvidenceReader is the host-supplied evidence query and subscription seam.
type EvidenceReader interface {
	QueryAll(ctx context.Context, ticketID string) ([]EvidenceEntry, error)
	Watch(ctx context.Context, ticketID string, emit func(EvidenceEntry) error) error
}

// ErrEvidenceUnwired refuses evidence reads until the host installs a reader.
var ErrEvidenceUnwired = cascade.New(cascade.KindUnavailable, "pbd evidence is not wired in this build")

// ErrEvidenceReaderNil rejects a nil reader without replacing the current one.
var ErrEvidenceReaderNil = cascade.New(cascade.KindInvalidInput, "pbd: evidence reader must not be nil")

// ErrEvidenceTicketRequired reports a missing evidence ticket argument.
var ErrEvidenceTicketRequired = cascade.New(cascade.KindInvalidInput, "pbd evidence: a ticket argument is required")

var evidenceReaderState = struct {
	sync.RWMutex
	reader EvidenceReader
}{reader: refusingEvidenceReader{}}

type refusingEvidenceReader struct{}

// QueryAll refuses reads when no host reader has been installed.
func (refusingEvidenceReader) QueryAll(context.Context, string) ([]EvidenceEntry, error) {
	return nil, ErrEvidenceUnwired
}

// Watch refuses subscriptions when no host reader has been installed.
func (refusingEvidenceReader) Watch(context.Context, string, func(EvidenceEntry) error) error {
	return ErrEvidenceUnwired
}

// SetEvidenceReader installs the host's reader. A nil reader is refused.
func SetEvidenceReader(r EvidenceReader) error {
	if r == nil {
		return ErrEvidenceReaderNil
	}
	evidenceReaderState.Lock()
	defer evidenceReaderState.Unlock()
	evidenceReaderState.reader = r
	return nil
}

func snapshotEvidenceReader() EvidenceReader {
	evidenceReaderState.RLock()
	defer evidenceReaderState.RUnlock()
	return evidenceReaderState.reader
}

func queryEvidence(ctx context.Context, reader EvidenceReader, ticket string) ([]EvidenceEntry, error) {
	if ticket == "" {
		return nil, ErrEvidenceTicketRequired
	}
	rows, err := reader.QueryAll(ctx, ticket)
	if err != nil {
		return nil, err
	}
	result := make([]EvidenceEntry, len(rows))
	for i, row := range rows {
		result[i] = renderableEvidence(row)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Seq < result[j].Seq })
	return result, nil
}

func renderableEvidence(row EvidenceEntry) EvidenceEntry {
	if row.Redacted {
		row.ArtifactRef = ""
	}
	return row
}

// EvidenceRPC handles evidence tool/RPC requests with a required ticket field.
// It returns a sorted array, including an empty array for an empty ledger.
func EvidenceRPC(ctx context.Context, params json.RawMessage) (any, error) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "pbd evidence: malformed request")
	}
	return queryEvidence(ctx, snapshotEvidenceReader(), req.Ticket)
}
