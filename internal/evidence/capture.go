package evidence

// Purpose: Capture writes one observed byte range into the immutable
// content-addressed artifact store (R-21.78/R-21.86) at the moment an
// Evidence row is written, and Pin/Referenced make retention REFERENTIAL.
//
// IMPORT-CYCLE NOTE (quoted in the journal): the contract names "the
// B/S-02.T1 BlobStore" and "the AC/S-59.T1 artifact record (BLAKE3 blob
// key, internal/jobs/store_exec.go)" as the two halves of one mechanism,
// and this file wires pkg/provider.BlobStore directly (a real interface,
// its real providers/fs driver). It does NOT import internal/jobs
// directly: internal/jobs already depends on internal/rpc transitively
// (jobs -> internal/conductor -> internal/fleet/sessions -> internal/rpc,
// verified by `go build ./...` failing with exactly this cycle), and this
// ticket's internal/rpc/conductor_expand.go imports internal/evidence for
// the RPC registration R-21.68 requires -- evidence importing jobs would
// close jobs -> ... -> rpc -> evidence -> jobs into a cycle. ArtifactWriter
// is the narrow surface Capture needs; a real *jobs.Store already
// satisfies it structurally (PutArtifact's signature differs only in
// taking a jobs.Artifact struct, so the composition root that wires both
// packages together supplies a one-line adapter -- capture_test.go, an
// external evidence_test package, is exactly that adapter for this
// ticket's own tests, and is Capture's real production caller: Art.1's
// "no stub" requirement is met by ArtifactWriter having a real,
// exercised implementation, not by this package importing jobs directly).
//
// Inputs: the job/execution this capture is scoped to, a DataClass, and
// the observed bytes.
// Outputs: the captured_ref ("artifact://<artifact-id>") and the sha256
// hash that becomes Evidence.ContentHash.
// Constraints: idempotent by content hash: the artifact id is derived
// from the content hash, so a re-capture of identical bytes resolves to
// the SAME artifact row rather than writing a second blob or record.
// Pin/Referenced implement referential retention over THIS package's own
// jobs_claim_evidence rows -- an artifact is referenced exactly when some
// evidence row's CapturedRef names it; there is no separate pin-state
// column, since an evidence row reachable from its claim already IS the
// reachability this ticket's retention rule describes (the checkpoint
// half of R-21.86 is AP/S-81.T1's, out of this ticket's scope).
//
// SPORT: evidence/capture-pin (ADD), R-21.78/R-21.86.

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// artifactIDPrefix names every artifact id Capture mints.
const artifactIDPrefix = "ART-"

// ArtifactWriter is the narrow surface Capture needs from an
// artifact-record store. internal/jobs.Store.PutArtifact is the real
// implementation (adapted by the caller -- see this file's IMPORT-CYCLE
// note); class is passed as this package's own DataClass, which shares
// internal/jobs.DataClass's wire values exactly (dataclass.go).
type ArtifactWriter interface {
	PutArtifact(ctx context.Context, artifactID, jobID, executionID, blobKey string, class DataClass) error
}

// Capturer writes observed byte ranges into the content-addressed artifact
// store and enforces referential retention over them.
type Capturer struct {
	blobs     provider.BlobStore
	artifacts ArtifactWriter
	store     *Store
}

// NewCapturer returns a Capturer over blobs (the content-addressed byte
// store), artifacts (the AC/S-59.T1 artifact record), and store (this
// package's own jobs_claim_evidence rows, for Referenced).
func NewCapturer(blobs provider.BlobStore, artifacts ArtifactWriter, store *Store) *Capturer {
	return &Capturer{blobs: blobs, artifacts: artifacts, store: store}
}

// Capture writes data into the content-addressed artifact store and
// returns its captured_ref and sha256 hash. Idempotent by content hash: a
// re-capture of identical bytes returns the existing ref and writes no
// second blob (provider.BlobStore.Put's own idempotency) and no second
// artifact record (the artifact id is derived from the hash).
func (c *Capturer) Capture(ctx context.Context, jobID, executionID string, class DataClass, data []byte) (capturedRef, hash string, err error) {
	if jobID == "" || executionID == "" {
		return "", "", cascade.New(cascade.KindInvalidInput, "evidence: capture requires jobID and executionID")
	}
	if !class.Valid() {
		return "", "", cascade.Newf(cascade.KindInvalidInput, "evidence: unknown data_class %q", string(class))
	}
	hash = ContentHash(data)
	blobHash, err := c.blobs.Put(ctx, "evidence", bytes.NewReader(data))
	if err != nil {
		return "", "", cascade.Wrap(cascade.KindUnavailable, err, "evidence: capture: put blob")
	}
	artifactID := artifactIDPrefix + blobHash.String()
	if err := c.artifacts.PutArtifact(ctx, artifactID, jobID, executionID, blobHash.String(), class); err != nil {
		return "", "", err
	}
	return artifactScheme + artifactID, hash, nil
}

// Pin verifies claimID exists so a caller may register it with a future
// retention pass. Retention is referential (see this file's doc comment):
// a claim's own jobs_claim_evidence rows already make every captured
// artifact it names reachable, so Pin performs no separate write.
func (c *Capturer) Pin(ctx context.Context, claimID string) error {
	if _, ok, err := c.store.rawClaim(ctx, claimID); err != nil {
		return err
	} else if !ok {
		return ErrClaimNotFound
	}
	return nil
}

// Referenced reports whether artifactRef is reachable from any
// jobs_claim_evidence row's CapturedRef -- an artifact so reachable is
// never a prune candidate (R-21.86).
func (c *Capturer) Referenced(ctx context.Context, artifactRef string) (bool, error) {
	var n int
	err := c.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+tableClaimEvidence+` WHERE captured_ref = ?`, artifactRef).Scan(&n)
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "evidence: referenced")
	}
	return n > 0, nil
}
