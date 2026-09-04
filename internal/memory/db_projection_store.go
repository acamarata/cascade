package memory

// Purpose: the projection's own storage half: reading and dropping the
//   stored rows, stamping the header that decides whether a stored
//   projection is readable at all, and the term-index query that turns a
//   search into a set of addresses. Split from db_projection.go because
//   both files must stay under the 300-line cap the repo gate enforces.
// Inputs: a pkg/provider.Store scoped to the memory domain namespace.
// Outputs: rows, addresses, or a pkg/cascade taxonomy error.
// Constraints: every read fails closed. A stored row this build cannot
//   parse is refused rather than half-read, and the answer to a refusal is
//   always Rebuild, never a repair written back to a file.
// SPORT: G/memory-db-projection (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Search returns the rows whose indexed terms contain every term of query,
// in lexical address order.
//
// A row is metadata only. The caller reads each hit back through the store
// to obtain the record, so the index can never disclose the content of a
// record the store itself would refuse, and a stale row costs a wasted
// read rather than a wrong answer. An empty query matches nothing rather
// than everything: widening on an input the tokenizer found nothing in
// would be the one failure mode a search index must not have.
func (j *ProjectionJob) Search(ctx context.Context, query string) ([]ProjectionRow, error) {
	terms := Tokenize(query)
	if len(terms) == 0 {
		return nil, nil
	}
	var addrs map[string]bool
	for i, term := range terms {
		hits, err := j.postings(ctx, term)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			addrs = hits
		} else {
			addrs = intersect(addrs, hits)
		}
		if len(addrs) == 0 {
			return nil, nil
		}
	}
	return j.rowsFor(ctx, addrs)
}

// postings returns every address indexed under one term.
func (j *ProjectionJob) postings(ctx context.Context, term string) (map[string]bool, error) {
	out := map[string]bool{}
	err := j.scan(ctx, termScanPrefix(term), func(key string, _ []byte) error {
		if addr, ok := addressFromPosting(key); ok {
			out[addr] = true
		}
		return nil
	})
	return out, err
}

// intersect keeps the addresses present in both sets.
func intersect(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for addr := range a {
		if b[addr] {
			out[addr] = true
		}
	}
	return out
}

// rowsFor loads the named rows in lexical address order. A row that has
// been retired is dropped from the answer: its postings are already gone,
// so reaching one here means the index and the row disagreed, and the
// row is the more recent of the two.
func (j *ProjectionJob) rowsFor(ctx context.Context, addrs map[string]bool) ([]ProjectionRow, error) {
	ordered := make([]string, 0, len(addrs))
	for addr := range addrs {
		ordered = append(ordered, addr)
	}
	sort.Strings(ordered)
	out := make([]ProjectionRow, 0, len(ordered))
	for _, addr := range ordered {
		data, err := j.db.Get(ctx, projNamespace, rowKey(addr))
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, storeFailure(err, "reading projected row "+addr)
		}
		row, err := decodeRow(data)
		if err != nil {
			return nil, err
		}
		if !row.Deleted {
			out = append(out, row)
		}
	}
	return out, nil
}

// loadRows reads every stored row into memory, keyed by address. The whole
// set is loaded once per run rather than read back per record, because a
// run compares every record it scans against its stored row and the
// alternative is one round trip per record.
func (j *ProjectionJob) loadRows(ctx context.Context) (map[string]ProjectionRow, error) {
	rows := map[string]ProjectionRow{}
	err := j.scan(ctx, rowPrefix, func(_ string, value []byte) error {
		row, decErr := decodeRow(value)
		if decErr != nil {
			return decErr
		}
		rows[row.Address] = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// dropAll removes every projection key. It is the first half of Rebuild,
// and it is safe precisely because nothing it deletes is a source of
// truth: every key it removes is derivable again from the files.
func (j *ProjectionJob) dropAll(ctx context.Context) error {
	var keys []string
	if err := j.scan(ctx, "proj:", func(key string, _ []byte) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := j.db.Delete(ctx, projNamespace, key); err != nil {
			return storeFailure(err, "dropping projection key "+key)
		}
	}
	return nil
}

// versionMismatch reports whether the stored projection was written by a
// different build of this schema, or is unreadable.
//
// Both answers are the same answer: rebuild. A projection this build
// cannot read is not an integrity incident, because the files still hold
// every record; treating it as one would turn a cheap rebuild into an
// outage.
func (j *ProjectionJob) versionMismatch(ctx context.Context) (bool, error) {
	data, err := j.db.Get(ctx, projNamespace, metaKey)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, storeFailure(err, "reading projection header")
	}
	var meta projectionMeta
	if json.Unmarshal(data, &meta) != nil {
		return true, nil
	}
	return meta.Version != ProjectionVersion, nil
}

// writeMeta stamps the header with this build's version and the injected
// clock's instant. It is the only clock reading the projection stores, and
// it lives outside the rows so that two rebuilds of the same files produce
// byte-identical rows whatever the clock says.
func (j *ProjectionJob) writeMeta(ctx context.Context) error {
	data, err := json.Marshal(projectionMeta{
		Version:             ProjectionVersion,
		ProjectedAtUnixNano: j.clock.Now().UTC().UnixNano(),
	})
	if err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"encoding projection header: %v", err)
	}
	if err := j.db.Put(ctx, projNamespace, metaKey, data); err != nil {
		return storeFailure(err, "writing projection header")
	}
	return nil
}

// scan walks every key under prefix, handing each to visit. The iterator
// is closed on every path, including an early error return.
func (j *ProjectionJob) scan(ctx context.Context, prefix string,
	visit func(key string, value []byte) error) error {
	it, err := j.db.Scan(ctx, projNamespace, prefix)
	if err != nil {
		return storeFailure(err, "scanning projection prefix "+prefix)
	}
	defer func() { _ = it.Close() }()
	for it.Next(ctx) {
		if visitErr := visit(it.Key(), it.Value()); visitErr != nil {
			return visitErr
		}
	}
	if err := it.Err(); err != nil {
		return storeFailure(err, "scanning projection prefix "+prefix)
	}
	return nil
}

// isNotFound reports whether err is the store's "no such key" answer.
func isNotFound(err error) bool {
	var cerr *cascade.Error
	return errors.As(err, &cerr) && cerr.Kind == cascade.KindNotFound
}

// storeFailure converts a backing-store error into this package's typed
// I/O refusal, so no raw driver error escapes the memory API.
func storeFailure(err error, what string) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO, "%s: %v", what, err)
}
