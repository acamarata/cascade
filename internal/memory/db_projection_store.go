package memory

// Purpose: the projection's storage access: reading and writing rows,
//   postings and the layout stamp through the injected provider.Store,
//   retiring or withdrawing a record, dropping the projection whole, and
//   answering a query from the postings.
// Inputs: an injected provider.Store, rows built by db_projection.go, and
//   the instant a query judges a TTL against.
// Outputs: stored rows and postings, matched records, or a pkg/cascade
//   taxonomy error.
// Constraints: split from db_projection.go and schema.go only to keep
//   every file inside the 300-line cap; no behaviour lives here that the
//   projection's own doc comment does not describe. No direct SQL: every
//   write goes through the injected store, which is the single write
//   executor for the whole database.
// SPORT: G/memory-db-projection (ADD, P1-E07-W2-S13-T2).

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// scanKeys returns every key under prefix in the projection namespace, in
// key order. It drains the iterator and closes it on every path, including
// an early error return.
func scanKeys(ctx context.Context, kv provider.Store, prefix string) ([]string, error) {
	it, err := kv.Scan(ctx, projectionNamespace, prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var out []string
	for it.Next(ctx) {
		out = append(out, it.Key())
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// searchIndex answers a full-text query from the postings.
//
// A record matches when it carries EVERY token of the query (conjunctive),
// which is the reading that cannot return more than the caller asked for.
// An empty query matches nothing rather than everything: a query that
// widened to "all records" when its terms tokenized away would disclose
// records the caller never asked to see. Results are ordered by record id,
// so the same query over the same projection returns the same order on any
// machine. at judges each row's TTL and comes from the caller's clock.
func searchIndex(
	ctx context.Context, kv provider.Store, query string, at time.Time, limit int,
) ([]IndexedRecord, error) {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	ids, err := matchingIDs(ctx, kv, tokens)
	if err != nil {
		return nil, err
	}
	out := make([]IndexedRecord, 0, len(ids))
	for _, id := range ids {
		row, found, rerr := readRow(ctx, kv, id)
		if rerr != nil {
			return nil, rerr
		}
		if !found || !row.Visible(at) {
			continue
		}
		out = append(out, row)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// matchingIDs returns the sorted record ids carrying every token.
func matchingIDs(ctx context.Context, kv provider.Store, tokens []string) ([]string, error) {
	var acc map[string]bool
	for i, tok := range tokens {
		keys, err := scanKeys(ctx, kv, tokenScanPrefix(tok))
		if err != nil {
			return nil, err
		}
		hits := make(map[string]bool, len(keys))
		for _, k := range keys {
			hits[strings.TrimPrefix(k, tokenScanPrefix(tok))] = true
		}
		if i == 0 {
			acc = hits
		} else {
			for id := range acc {
				if !hits[id] {
					delete(acc, id)
				}
			}
		}
		if len(acc) == 0 {
			return nil, nil
		}
	}
	out := make([]string, 0, len(acc))
	for id := range acc {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// readRow returns the projected row for id. A missing row is not an error:
// the projection is derived and may simply not have caught up yet, and
// reporting "no row" is what lets a caller fall back to the files.
func readRow(ctx context.Context, kv provider.Store, id string) (IndexedRecord, bool, error) {
	data, err := kv.Get(ctx, projectionNamespace, recordKey(id))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return IndexedRecord{}, false, nil
		}
		return IndexedRecord{}, false, err
	}
	row, derr := decodeRow(data)
	if derr != nil {
		return IndexedRecord{}, false, derr
	}
	return row, true, nil
}

// retireMissing marks the rows of records the files no longer hold.
func (j *ProjectionJob) retireMissing(
	ctx context.Context, kind MemoryKind, live map[string]bool, now time.Time, res *ProjectionResult,
) error {
	keys, err := scanKeys(ctx, j.kv, kindRowPrefix(kind))
	if err != nil {
		return wrapKV(err, "scanning projected rows for kind "+string(kind))
	}
	for _, key := range keys {
		id := key[len(recordPrefix):]
		if live[id] {
			continue
		}
		row, found, rerr := readRow(ctx, j.kv, id)
		if rerr != nil || !found {
			if werr := j.withdraw(ctx, id); werr != nil {
				return werr
			}
			continue
		}
		if row.Deleted {
			continue
		}
		if derr := j.retire(ctx, row, now); derr != nil {
			return derr
		}
		res.Retired++
	}
	return nil
}

// retire keeps the row, marked deleted, and removes everything that could
// still answer a query for it: its postings and its vector.
func (j *ProjectionJob) retire(ctx context.Context, row IndexedRecord, now time.Time) error {
	if err := j.deletePostings(ctx, row); err != nil {
		return err
	}
	if err := j.deleteVector(ctx, row.ID); err != nil {
		return err
	}
	row.Deleted, row.Tokens, row.Body = true, nil, ""
	row.IndexedAtUnixNano = now.UnixNano()
	return j.writeRow(ctx, row)
}

// withdraw removes a record from the projection entirely: its row, its
// postings and its vector. It is what a refused record gets, so nothing
// the file store will not return can be reached through the index.
func (j *ProjectionJob) withdraw(ctx context.Context, id string) error {
	row, found, err := readRow(ctx, j.kv, id)
	if err == nil && found {
		if perr := j.deletePostings(ctx, row); perr != nil {
			return perr
		}
	}
	if derr := j.deleteVector(ctx, id); derr != nil {
		return derr
	}
	if derr := j.kv.Delete(ctx, projectionNamespace, recordKey(id)); derr != nil {
		return wrapKV(derr, "deleting projected row "+id)
	}
	return nil
}

func (j *ProjectionJob) deleteVector(ctx context.Context, id string) error {
	if j.vectors == nil {
		return nil
	}
	if err := j.vectors.Delete(ctx, projectionNamespace, []string{id}); err != nil {
		return wrapKV(err, "deleting vector "+id)
	}
	return nil
}

// writeRow stores a row and the postings its token set names.
func (j *ProjectionJob) writeRow(ctx context.Context, row IndexedRecord) error {
	data, err := encodeRow(row)
	if err != nil {
		return err
	}
	if perr := j.kv.Put(ctx, projectionNamespace, recordKey(row.ID), data); perr != nil {
		return wrapKV(perr, "writing projected row "+row.ID)
	}
	for _, tok := range row.Tokens {
		if perr := j.kv.Put(ctx, projectionNamespace, tokenKey(tok, row.ID), postingValue); perr != nil {
			return wrapKV(perr, "writing posting for "+row.ID)
		}
	}
	return nil
}

// deletePostings retracts exactly the postings a stored row recorded.
func (j *ProjectionJob) deletePostings(ctx context.Context, row IndexedRecord) error {
	for _, tok := range row.Tokens {
		if err := j.kv.Delete(ctx, projectionNamespace, tokenKey(tok, row.ID)); err != nil {
			return wrapKV(err, "deleting posting for "+row.ID)
		}
	}
	return nil
}

// clear drops every key the projection owns, including the version stamp,
// leaving the rest of the memory namespace untouched.
func (j *ProjectionJob) clear(ctx context.Context) error {
	keys, err := scanKeys(ctx, j.kv, projectionPrefix)
	if err != nil {
		return wrapKV(err, "scanning the projection")
	}
	for _, key := range keys {
		if derr := j.kv.Delete(ctx, projectionNamespace, key); derr != nil {
			return wrapKV(derr, "clearing the projection")
		}
	}
	return nil
}

// readVersion returns the stored layout version, or 0 when the projection
// has never been stamped or carries a stamp this build cannot read. Zero
// is never a valid ProjectionVersion, so an unreadable stamp rebuilds
// rather than being trusted.
func (j *ProjectionJob) readVersion(ctx context.Context) (int, error) {
	data, err := j.kv.Get(ctx, projectionNamespace, metaVersionKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return 0, nil
		}
		return 0, wrapKV(err, "reading the projection version")
	}
	v, cerr := strconv.Atoi(string(data))
	if cerr != nil {
		return 0, nil
	}
	return v, nil
}

// writeVersion stamps this build's layout version on the projection.
func (j *ProjectionJob) writeVersion(ctx context.Context) error {
	if err := j.kv.Put(ctx, projectionNamespace, metaVersionKey, []byte(strconv.Itoa(ProjectionVersion))); err != nil {
		return wrapKV(err, "writing the projection version")
	}
	return nil
}

// wrapKV turns a storage failure into this package's own I/O refusal, so a
// caller matches one sentinel whether the files or the projection failed.
func wrapKV(err error, what string) error {
	if err == nil {
		return nil
	}
	return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO, "%s: %v", what, err)
}
