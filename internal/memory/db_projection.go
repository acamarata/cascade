package memory

// Purpose: ProjectionJob, the derived read index over the file store. It
//   walks every ratified kind, projects each live record into a row plus
//   its term postings inside the memory domain namespace, retires records
//   the files no longer have, and keeps a vector index in step with the
//   bodies it projected.
// Inputs: a MemoryStore (the files, which are the source of truth), a
//   pkg/provider.Store (whose SQLite driver routes every write through the
//   single write-connection executor), optional Embedder and VectorStore
//   seams, and an injected Clock.
// Outputs: a ProjectionResult counting what happened, or a pkg/cascade
//   taxonomy error for a failure that stopped the whole run.
// Constraints: the projection is DERIVED STATE. It can be deleted,
//   corrupted or truncated and rebuilt from the files alone, and when the
//   two disagree the file wins. Nothing here ever writes a file back from
//   a row. No bare time.Now. One damaged record fails that record only.
// SPORT: G/memory-db-projection (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProjectionResult reports what one run did. Counts are per record, and
// Scanned is every live record the files offered, so Scanned equals
// Upserted plus Skipped plus Failed.
type ProjectionResult struct {
	// Scanned is how many live records the file store listed.
	Scanned int
	// Upserted is how many rows were written or rewritten.
	Upserted int
	// Skipped is how many records were already projected at the same
	// content hash and cost no write at all.
	Skipped int
	// Retired is how many rows were marked deleted because the files no
	// longer hold the record (a tombstone, or a file removed by hand).
	Retired int
	// Failed is how many records could not be projected. Each one is a
	// record this build could not read whole, and each is named in
	// Failures. A failure does not stop the run.
	Failed int
	// Failures names the addresses in Failed, in lexical order.
	Failures []string
	// VectorsUpserted and VectorsDeleted count the vector index writes
	// this run made. Both stay zero when no Embedder is wired.
	VectorsUpserted int
	VectorsDeleted  int
	// Rebuilt reports that Run found a stored projection this build could
	// not read and dropped it before projecting.
	Rebuilt bool
}

// ProjectionJob projects the file store into the memory domain's read
// index.
//
// The index is an accelerator, never an authority. A record absent from it
// is not absent from the store: a file this build cannot parse is counted
// as a failure and left unindexed, and the record still exists, is still
// listed, and is still readable (or still refused) through the store
// itself. A caller that must not miss a record enumerates the store; a
// caller that wants speed searches here and reads each hit back through
// the store, which is also what keeps the index from widening what a query
// may see: a row carries no body, so every hit is re-read under the file
// store's own fail-closed rules before its content reaches anyone.
type ProjectionJob struct {
	files    MemoryStore
	db       provider.Store
	embedder provider.Embedder
	vectors  provider.VectorStore
	clock    Clock
}

// NewProjectionJob returns a job projecting files into db, stamping its
// header from clk. embedder and vectors are optional and must be supplied
// together: with both, every projected body is embedded and every retired
// record's vector deleted; with neither, the run keeps the row and term
// index only. A nil pair is a documented configuration, not a fault.
func NewProjectionJob(files MemoryStore, db provider.Store, embedder provider.Embedder,
	vectors provider.VectorStore, clk Clock) *ProjectionJob {
	return &ProjectionJob{files: files, db: db, embedder: embedder, vectors: vectors, clock: clk}
}

// Run brings the projection up to date with the files.
//
// It is idempotent: a second run over unchanged files writes nothing and
// reports every record as skipped. A stored projection stamped with a
// version this build does not know is not read and not migrated; it is
// dropped and rebuilt, because the files can always produce it again.
func (j *ProjectionJob) Run(ctx context.Context) (ProjectionResult, error) {
	stale, err := j.versionMismatch(ctx)
	if err != nil {
		return ProjectionResult{}, err
	}
	if stale {
		return j.Rebuild(ctx)
	}
	return j.project(ctx, false)
}

// Rebuild drops every projection key and projects the files from scratch.
// It is the answer to any disagreement between the index and the files,
// and the only repair this package has, because nothing in the index is
// needed to reconstruct a record.
func (j *ProjectionJob) Rebuild(ctx context.Context) (ProjectionResult, error) {
	if err := j.dropAll(ctx); err != nil {
		return ProjectionResult{}, err
	}
	res, err := j.project(ctx, true)
	res.Rebuilt = true
	return res, err
}

// project walks every kind, projects each live record, retires the rows
// the files no longer back, and stamps the header.
func (j *ProjectionJob) project(ctx context.Context, fresh bool) (ProjectionResult, error) {
	var res ProjectionResult
	rows := map[string]ProjectionRow{}
	if !fresh {
		loaded, err := j.loadRows(ctx)
		if err != nil {
			return res, err
		}
		rows = loaded
	}
	seen := map[string]bool{}
	for _, kind := range AllKinds() {
		names, err := j.files.List(ctx, kind)
		if err != nil {
			return res, err
		}
		for _, name := range names {
			addr := Address(kind, name)
			seen[addr] = true
			j.projectOne(ctx, kind, name, rows[addr], &res)
		}
	}
	if err := j.retireUnseen(ctx, rows, seen, &res); err != nil {
		return res, err
	}
	sort.Strings(res.Failures)
	return res, j.writeMeta(ctx)
}

// projectOne projects a single record, recording the outcome in res.
//
// A record this build cannot read whole is counted as a failure and left
// alone: no row is written for it, and any row it already had is left
// exactly as it was rather than rewritten from a record nobody could
// parse. That is what keeps one damaged file from failing a whole run, and
// what keeps a record the store refuses from appearing in the index as
// though the store had returned it.
func (j *ProjectionJob) projectOne(ctx context.Context, kind MemoryKind, name string,
	prev ProjectionRow, res *ProjectionResult) {
	res.Scanned++
	addr := Address(kind, name)
	entry, err := j.files.Read(ctx, kind, name)
	if err != nil {
		res.Failed++
		res.Failures = append(res.Failures, addr)
		return
	}
	row := rowFor(entry)
	if prev.Address == addr && !prev.Deleted && prev.ContentHash == row.ContentHash {
		res.Skipped++
		return
	}
	if err := j.writeRow(ctx, prev, row); err != nil {
		res.Failed++
		res.Failures = append(res.Failures, addr)
		return
	}
	res.Upserted++
	if err := j.upsertVector(ctx, row, entry.Body); err != nil {
		res.Failed++
		res.Failures = append(res.Failures, addr)
		return
	}
	if j.embedder != nil {
		res.VectorsUpserted++
	}
}

// writeRow replaces one row and its postings in a single transaction, so
// no reader ever sees a row whose postings belong to an older body.
func (j *ProjectionJob) writeRow(ctx context.Context, prev, row ProjectionRow) error {
	data, err := encodeRow(row)
	if err != nil {
		return err
	}
	return j.db.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		for _, term := range prev.Terms {
			if err := tx.Delete(ctx, projNamespace, termKey(term, row.Address)); err != nil {
				return err
			}
		}
		for _, term := range row.Terms {
			if err := tx.Put(ctx, projNamespace, termKey(term, row.Address), nil); err != nil {
				return err
			}
		}
		return tx.Put(ctx, projNamespace, rowKey(row.Address), data)
	})
}

// retireUnseen marks every row the files no longer back as deleted and
// removes its postings, so a retired record can never be returned by a
// search. The row itself is kept, which is how a reader tells a record
// that was retired from one that was never projected.
func (j *ProjectionJob) retireUnseen(ctx context.Context, rows map[string]ProjectionRow,
	seen map[string]bool, res *ProjectionResult) error {
	addrs := make([]string, 0, len(rows))
	for addr := range rows {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	for _, addr := range addrs {
		row := rows[addr]
		if seen[addr] || row.Deleted {
			continue
		}
		if err := j.retire(ctx, row); err != nil {
			return err
		}
		res.Retired++
		if j.embedder != nil {
			res.VectorsDeleted++
		}
	}
	return nil
}

// retire writes the deleted row, drops its postings, and deletes its
// vector.
func (j *ProjectionJob) retire(ctx context.Context, row ProjectionRow) error {
	dead := row
	dead.Deleted = true
	dead.Terms = nil
	if err := j.writeRow(ctx, row, dead); err != nil {
		return err
	}
	if j.vectors == nil || j.embedder == nil {
		return nil
	}
	if err := j.vectors.Delete(ctx, projNamespace, []string{row.Address}); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"deleting vector for %s: %v", row.Address, err)
	}
	return nil
}

// upsertVector embeds a projected body and writes it to the vector index.
// It is a no-op when no Embedder is wired, which is the configuration in
// which the projection is a row and term index only.
func (j *ProjectionJob) upsertVector(ctx context.Context, row ProjectionRow, body string) error {
	if j.embedder == nil || j.vectors == nil {
		return nil
	}
	inputs := []provider.EmbedInput{{Text: body}}
	out, err := j.embedder.Embed(ctx, inputs)
	if err != nil {
		return err
	}
	if !j.embedder.Model().ValidBatch(inputs, out) {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"embedder returned a batch that does not match model %s for %s",
			j.embedder.Model().ID, row.Address)
	}
	vec := provider.Vector{ID: row.Address, Values: out[0].Vector, Metadata: map[string]any{
		"kind": string(row.Kind), "name": row.Name, "content_hash": row.ContentHash,
	}}
	if err := j.vectors.Upsert(ctx, projNamespace, []provider.Vector{vec}); err != nil {
		return err
	}
	return nil
}
