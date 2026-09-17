package sync

import (
	"context"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the receiving half — the mirror of SendBatch, and
//
//	the blob path that turns arriving chunks into an ADMITTED blob.
//
// WHY THE MERGE LAYER NEEDS THIS. The content-addressed union carries
//
//	admitted blobs only, and "admitted" is a fact only this path can
//	establish: the bytes have been written, the digest has been recomputed
//	over them, and it matched the address they were declared under. A
//	receive path that skipped that would let a truncated transfer or a
//	substituted payload enter the store under a digest that does not
//	describe it, and every later reader would trust the name.
//
// RESUME IS THE ORDINARY CASE, not the exception. A sync between two
//
//	machines on domestic connections drops mid-transfer routinely, so the
//	blob path asks the staging layer where it got to and continues from
//	there. Beginning again from zero on every reconnect would make a large
//	blob impossible to transfer at all over a link that drops.
//
// NOTHING IS ADMITTED ON A PARTIAL STREAM. Admission happens after the
//
//	last chunk and only then; a stream that stopped early leaves staged
//	bytes and a cursor, which is exactly what the next attempt resumes
//	from.
//
// Inputs: a connection, and for blobs a staging directory and the address.
// Outputs: the decoded records, or an admitted blob at its destination.
// SPORT: internal/sync receive path (ADD) — P1-E17-W4-S38-T2.

// ReceiveBatch reads one record batch from conn and decodes it.
//
// The mirror of SendBatch, and deliberately NOT its inverse: the sender
// filters, journals exclusions and advances its cursor, none of which the
// receiver repeats. A receiver that re-applied the sender's filter would
// be deciding a second time what the sender already decided, and the two
// could disagree.
func (e *Engine) ReceiveBatch(
	ctx context.Context, conn io.Reader, streamID, fromSeq uint64,
) ([]Record, uint64, error) {
	payload, received, err := ReceiveBytes(ctx, conn, streamID, fromSeq)
	if err != nil {
		return nil, received, err
	}
	var records []Record
	if err := json.Unmarshal(payload, &records); err != nil {
		return nil, received, cascade.Wrap(cascade.KindIntegrity, err,
			"sync: the received batch is not a record array")
	}
	return records, received, nil
}

// BlobTransfer is one blob arriving.
type BlobTransfer struct {
	// StagingDir is where partial bytes live until admission.
	StagingDir string
	// DestPath is where an admitted blob lands.
	DestPath string
	// Address is the content address the sender declared. It is checked
	// against the bytes at admission, never trusted before that.
	Address ContentAddress
	// Total is how many chunks the blob is.
	Total uint64
	// StreamID identifies the chunk stream.
	StreamID uint64
}

// ReceiveBlob receives one blob, resuming a partial transfer when there is
// one, and admits it only when the whole stream has arrived.
//
// The returned BlobRef reports Admitted, which is what the union requires.
// A partial transfer returns the error and no ref: there is no such thing
// as a half-admitted blob.
func (e *Engine) ReceiveBlob(ctx context.Context, conn io.Reader, t BlobTransfer) (BlobRef, error) {
	fromSeq, err := e.beginOrResume(t)
	if err != nil {
		return BlobRef{}, err
	}
	sink := func(seq, total uint64, payload []byte) error {
		return AppendStagedChunk(t.StagingDir, t.Address, seq, total, payload)
	}
	if _, err := ReceiveStream(ctx, conn, t.StreamID, fromSeq, sink); err != nil {
		// The staged bytes and the cursor stay on disk deliberately: they
		// are what the next attempt resumes from. Discarding them here
		// would turn every dropped connection into a restart.
		return BlobRef{}, err
	}
	if err := AdmitBlob(t.StagingDir, t.Address, t.DestPath); err != nil {
		return BlobRef{}, err
	}
	return BlobRef{Address: t.Address.Hex(), Admitted: true}, nil
}

// beginOrResume returns the sequence to start receiving from.
//
// It asks the staging layer first. A transfer with nothing staged begins
// at zero; one with staged bytes continues from the last chunk that was
// durably appended — which is the last one the sender can safely assume
// arrived.
//
// Only a NOT-FOUND resume starts fresh. Every other refusal — a cursor
// that names a different address, a cursor and file that disagree about
// size — means there IS staged state and this process cannot make sense of
// it, and beginning again over those bytes would append a second copy of
// the blob onto the first. Refusing is recoverable by hand; the append is
// not, because the result hashes to nothing and nobody knows why.
func (e *Engine) beginOrResume(t BlobTransfer) (uint64, error) {
	lastAcked, err := ResumeStaging(t.StagingDir, t.Address)
	if err == nil {
		return lastAcked, nil
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		return 0, err
	}
	if err := BeginStaging(t.StagingDir, t.Address, t.Total); err != nil {
		return 0, err
	}
	return 0, nil
}

// ReceiveAndMerge is the join: receive a batch and merge it into the local
// side under the domain's own strategy.
//
// One function because the two are one operation from a caller's point of
// view, and because separating them invites a caller to receive a batch
// and then choose its own merge — which is the mistake Engine.Merge exists
// to prevent.
func (e *Engine) ReceiveAndMerge(
	ctx context.Context, conn io.Reader, req MergeRequest, streamID, fromSeq uint64,
) (MergeResult, error) {
	records, _, err := e.ReceiveBatch(ctx, conn, streamID, fromSeq)
	if err != nil {
		return MergeResult{}, err
	}
	incoming := make(map[string]Record, len(records))
	for _, r := range records {
		if r.Domain != req.Domain || r.Subkind != req.Subkind {
			return MergeResult{}, errBatchDomainMismatch(req.Domain, req.Subkind, r)
		}
		incoming[r.ID] = r
	}
	req.Server = incoming
	return e.Merge(ctx, req)
}

// errBatchDomainMismatch refuses a batch carrying a record from a domain
// the caller did not ask for.
//
// Refused rather than filtered: a batch that mixed domains means the
// sender and receiver disagree about what is being synced, and merging the
// subset that matched would hide that while producing a result neither
// side expected.
func errBatchDomainMismatch(domain storage.DomainID, subkind string, got Record) error {
	return cascade.Newf(cascade.KindIntegrity,
		"sync: a batch for %s/%s carries record %q from %s/%s; the sender and this receiver "+
			"disagree about what is being synced", domain, subkind, got.ID, got.Domain, got.Subkind)
}
