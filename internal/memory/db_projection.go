package memory

// Purpose: ProjectionJob, the derived read side of the memory store: it
//   walks the file tree, projects every record into the memory domain's
//   key-value namespace with a full-text posting set and an embedding, and
//   retires the rows of records the files no longer have.
// Inputs: a MemoryStore (the files), an injected provider.Store (the
//   projection's storage), optional provider.Embedder and
//   provider.VectorStore for the vector leg, and a Clock.
// Outputs: a ProjectionResult counting what changed and naming every
//   record that could not be projected, or a pkg/cascade taxonomy error
//   when the run cannot proceed at all.
// Constraints: THE FILES ARE THE SOURCE OF TRUTH. Nothing here writes back
//   to a file, everything here can be deleted and rebuilt from the tree
//   alone, and on a disagreement the file wins. No bare clock, no direct
//   SQL, no map iteration reaching a stored value.
// SPORT: G/memory-db-projection (ADD, P1-E07-W2-S13-T2).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProjectionFailure names one record the run could not project, and why.
// A failure is reported, never swallowed: a record that drops out of the
// index without a word is the same defect as a listing that fails whole
// because of one damaged file, only quieter.
type ProjectionFailure struct {
	// ID is the record's "<kind>/<name>" address.
	ID string
	// Reason says what refused it, in the store's own vocabulary.
	Reason string
}

// ProjectionResult reports what one run did.
type ProjectionResult struct {
	// Scanned is how many live record names the run visited.
	Scanned int
	// Upserted is how many rows the run wrote because they were new or
	// had changed.
	Upserted int
	// Skipped is how many rows were already current, so nothing was
	// written for them. A second run over an unchanged tree is all skips.
	Skipped int
	// Retired is how many rows were marked deleted because the files no
	// longer hold the record.
	Retired int
	// Embedded is how many vectors were written.
	Embedded int
	// Failed is len(Failures), for callers that only want the count.
	Failed int
	// Failures names every record the run refused or could not read.
	Failures []ProjectionFailure
	// Rebuilt reports that the run dropped and rebuilt the whole
	// projection, because the stored layout version did not match this
	// build's ProjectionVersion.
	Rebuilt bool
}

// ProjectionJob projects the file-backed memory store into the memory
// domain's key-value namespace.
//
// The projection is derived state. It can be corrupted, truncated, or
// deleted outright, and a run rebuilds it from the files alone; nothing it
// holds is needed to reconstruct a record. A record the file store refuses
// to read (a damaged file, or a format version this build does not know)
// is refused here too, and any row it previously had is withdrawn, so the
// index can never serve content the store itself will not return. One such
// record costs exactly its own row: the run continues, and the refusal is
// reported in ProjectionResult.Failures.
type ProjectionJob struct {
	files    MemoryStore
	kv       provider.Store
	vectors  provider.VectorStore
	embedder provider.Embedder
	clock    Clock
}

// NewProjectionJob returns a job projecting files into kv, taking its
// timestamps from clk.
//
// vectors and embedder are the vector leg and are optional: when either is
// nil the run projects rows and postings only, which is what a profile
// with no embedding provider configured must still be able to do. Passing
// one without the other is treated as no vector leg, because half of it
// would write vectors nothing can query or query an index nothing fills.
func NewProjectionJob(
	files MemoryStore, kv provider.Store,
	embedder provider.Embedder, vectors provider.VectorStore, clk Clock,
) *ProjectionJob {
	return &ProjectionJob{files: files, kv: kv, vectors: vectors, embedder: embedder, clock: clk}
}

// Run brings the projection up to date with the files.
//
// It first compares the stored layout version with ProjectionVersion. A
// mismatch (including a projection that has never been stamped) means the
// rows on hand were written by a different layout and cannot be compared
// against what this build would write, so the run rebuilds instead of
// patching. That is always safe, because the files hold everything.
func (j *ProjectionJob) Run(ctx context.Context) (ProjectionResult, error) {
	current, err := j.readVersion(ctx)
	if err != nil {
		return ProjectionResult{}, err
	}
	if current != ProjectionVersion {
		return j.Rebuild(ctx)
	}
	return j.project(ctx, false)
}

// Rebuild drops every projected row and posting and projects the whole
// tree again. It is the documented answer to a projection that is stale,
// damaged, or written by another layout, and it is idempotent: two
// Rebuilds over the same files leave the same bytes, because every value
// written is either copied from a file or read from the injected clock.
func (j *ProjectionJob) Rebuild(ctx context.Context) (ProjectionResult, error) {
	if err := j.clear(ctx); err != nil {
		return ProjectionResult{}, err
	}
	res, err := j.project(ctx, true)
	if err != nil {
		return res, err
	}
	if err := j.writeVersion(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// Search returns the projected records matching query, most useful as the
// fast path a recall surface takes instead of re-reading every file.
//
// A hit is a pointer, not an authority: the row's body is what the file
// said when it was last projected, and the file wins on any disagreement.
// Retired and expired records are excluded, judged against the injected
// clock, so the index never returns a record the store itself would not.
func (j *ProjectionJob) Search(ctx context.Context, query string, limit int) ([]IndexedRecord, error) {
	return searchIndex(ctx, j.kv, query, j.clock.Now().UTC(), limit)
}

// project walks every kind and projects it. rebuilt is carried through to
// the result so a caller can tell a patched run from a rebuilt one.
func (j *ProjectionJob) project(ctx context.Context, rebuilt bool) (ProjectionResult, error) {
	res := ProjectionResult{Rebuilt: rebuilt}
	now := j.clock.Now().UTC()
	for _, kind := range AllKinds() {
		if err := j.projectKind(ctx, kind, now, &res); err != nil {
			return res, err
		}
	}
	res.Failed = len(res.Failures)
	return res, nil
}

// projectKind projects one kind's directory and retires the rows of
// records that are no longer live there.
//
// Deletion is detected by diffing the live listing against the rows on
// hand rather than by looking for tombstone files. The listing already
// treats a tombstone as "not live", and the diff additionally catches a
// record file deleted outright with no tombstone, which a tombstone scan
// would miss and leave in the index forever.
func (j *ProjectionJob) projectKind(
	ctx context.Context, kind MemoryKind, now time.Time, res *ProjectionResult,
) error {
	names, err := j.files.List(ctx, kind)
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(names))
	for _, name := range names {
		live[recordID(kind, name)] = true
		res.Scanned++
		if perr := j.projectOne(ctx, kind, name, now, res); perr != nil {
			return perr
		}
	}
	return j.retireMissing(ctx, kind, live, now, res)
}

// projectOne projects a single live record.
func (j *ProjectionJob) projectOne(
	ctx context.Context, kind MemoryKind, name string, now time.Time, res *ProjectionResult,
) error {
	id := recordID(kind, name)
	entry, err := j.files.Read(ctx, kind, name)
	if err != nil {
		if cascade.HasKind(err, cascade.KindCanceled) || cascade.HasKind(err, cascade.KindUnavailable) {
			return err
		}
		// A refusal the store itself makes (malformed, unsupported
		// format version, vanished between listing and read) must not
		// leave a row behind that keeps answering queries.
		res.Failures = append(res.Failures, ProjectionFailure{ID: id, Reason: err.Error()})
		return j.withdraw(ctx, id)
	}
	return j.upsert(ctx, entry, now, res)
}

// upsert writes a record's row, postings and vector, or does nothing when
// the row on hand already says exactly what this record says.
//
// The comparison is byte equality of the encoded row with the stored
// IndexedAt held fixed, the same rule FileStore.writeOver uses: comparing
// body hashes alone would treat a changed description or scope as "no
// change" and leave the index disagreeing with the file.
func (j *ProjectionJob) upsert(
	ctx context.Context, entry MemoryEntry, now time.Time, res *ProjectionResult,
) error {
	next := rowFor(entry, now)
	stored, found, err := readRow(ctx, j.kv, next.ID)
	if err != nil && !cascade.HasKind(err, cascade.KindIntegrity) {
		return err
	}
	if found && rowsEqual(stored, next) {
		res.Skipped++
		return nil
	}
	if found {
		if derr := j.deletePostings(ctx, stored); derr != nil {
			return derr
		}
	}
	next.EmbedModel = stored.EmbedModel
	reembed := !found || stored.ContentHash != next.ContentHash || stored.Deleted
	if reembed {
		model, eerr := j.embed(ctx, next)
		if eerr != nil {
			res.Failures = append(res.Failures, ProjectionFailure{ID: next.ID, Reason: eerr.Error()})
		} else if model != "" {
			next.EmbedModel = model
			res.Embedded++
		}
	}
	res.Upserted++
	return j.writeRow(ctx, next)
}

// rowFor builds the row a record projects to. Every field is copied from
// the record or derived from its body; the only value not from the files
// is IndexedAt, which comes from the injected clock.
func rowFor(e MemoryEntry, now time.Time) IndexedRecord {
	row := IndexedRecord{
		ID: recordID(e.Kind, e.Name), Name: e.Name, Kind: e.Kind,
		Description: e.Description, Body: e.Body,
		Origin: e.Provenance.Origin, SessionID: e.Provenance.SessionID,
		ScopeRef: e.ScopeRef, ContentHash: e.BodyHash(),
		CreatedAtUnixNano: e.Provenance.CreatedAt.UTC().UnixNano(),
		UpdatedAtUnixNano: e.Provenance.UpdatedAt.UTC().UnixNano(),
		Confidence:        e.Confidence, IndexedAtUnixNano: now.UnixNano(),
	}
	if e.ExpiresAt != nil {
		exp := e.ExpiresAt.UTC().UnixNano()
		row.ExpiresAtUnixNano = &exp
	}
	row.Tokens = rowTokens(row)
	return row
}

// rowsEqual compares two rows on everything except when they were
// indexed, which changes on every run and would otherwise make every run
// look like a change.
func rowsEqual(stored, next IndexedRecord) bool {
	next.IndexedAtUnixNano = stored.IndexedAtUnixNano
	next.EmbedModel = stored.EmbedModel
	a, aerr := encodeRow(stored)
	b, berr := encodeRow(next)
	return aerr == nil && berr == nil && string(a) == string(b)
}

// embed computes and stores this row's vector, returning the embedding
// model it was written in, or "" when no vector leg is configured.
func (j *ProjectionJob) embed(ctx context.Context, row IndexedRecord) (string, error) {
	if j.embedder == nil || j.vectors == nil {
		return "", nil
	}
	model := j.embedder.Model()
	inputs := []provider.EmbedInput{{Text: row.Body}}
	outs, err := j.embedder.Embed(ctx, inputs)
	if err != nil {
		return "", err
	}
	if !model.ValidBatch(inputs, outs) {
		return "", cascade.Newf(cascade.KindIntegrity,
			"embedder returned a batch that does not match model %s", model.ID)
	}
	vec := provider.Vector{ID: row.ID, Values: outs[0].Vector, Metadata: map[string]any{
		"kind": string(row.Kind), "name": row.Name,
		"scope_ref": row.ScopeRef, "content_hash": row.ContentHash,
	}}
	if err := j.vectors.Upsert(ctx, projectionNamespace, []provider.Vector{vec}); err != nil {
		return "", err
	}
	return model.ID, nil
}
