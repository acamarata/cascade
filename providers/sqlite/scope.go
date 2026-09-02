// Purpose: the domain-scoped provider.Store view — Driver.Scoped's return
//   type — that enforces this ticket's cross-domain capability check on
//   every Get/Put/Delete/Scan/Tx call. Split from driver.go under
//   R-14.117 (Art.10.3's 300-line cap authorizes in-package splits, the
//   same pattern tx.go and iterator.go already use): adding Scoped and its
//   supporting types directly to driver.go would push it well past 300
//   lines. This file joins driver.go's authorized write set automatically
//   per that ruling, and is the "symmetric read guard in
//   providers/sqlite/driver.go" the ticket's task text calls for —
//   physically split, same enforcement responsibility driver.go's package
//   doc now points to.
// Inputs: a *Driver, a domain string, and a GrantChecker (executor.go).
// Outputs: a provider.Store (and, inside Tx, a provider.Tx) that delegates
//   every same-domain call straight through to the underlying Driver and
//   fail-closed-denies every cross-domain call the checker does not
//   affirmatively grant.
// Constraints: enforcement compares namespace to domain by exact string
//   equality only — never a prefix/HasPrefix check — so a namespace that
//   merely starts with or contains the scoped domain's name (a "forged
//   prefix" bypass attempt) is still treated as a distinct, ungranted
//   domain. Scan is guarded exactly like Get: the whole namespace is
//   checked once before the query runs, so a cross-domain scan is refused
//   before a single row is ever read, closing the classic "iteration
//   leaks keys from another domain" hole.
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T5).

package sqlite

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/provider"
)

// Scoped returns a provider.Store bound to exactly one domain, backed by
// d's same underlying connections and write executor (so domainA and
// domainB handles for the same physical database come from one Driver,
// with no second flock/open needed). Every call whose namespace argument
// differs from domain is checked against checker before it touches the
// database; every call whose namespace equals domain passes straight
// through unchecked, exactly as same-domain access needs no grant per
// internal/storage.CapabilityRegistry.Check's own same-domain shortcut. A
// nil checker is not rejected here — it is accepted and will fail closed
// on the first actual cross-domain call (submitScoped / checkAccess both
// treat nil as "deny"), so a caller who forgets to wire a checker gets an
// explicit ErrScopeDenied at the point of the first cross-domain access
// rather than a panic at construction time.
func (d *Driver) Scoped(domain string, checker GrantChecker) provider.Store {
	return &scopedStore{d: d, domain: domain, checker: checker}
}

// scopedStore is Driver.Scoped's provider.Store implementation.
type scopedStore struct {
	d       *Driver
	domain  string
	checker GrantChecker
}

var _ provider.Store = (*scopedStore)(nil)

// checkAccess enforces the read guard: nil for a same-domain namespace,
// otherwise the outcome of checker.Check (with a nil checker denied
// exactly as submitScoped denies a nil checker for writes — see
// ErrScopeDenied's doc). Shared by Get and Scan below.
func (s *scopedStore) checkAccess(ctx context.Context, namespace string, op CapOp) error {
	if namespace == s.domain {
		return nil
	}
	if s.checker == nil {
		return ErrScopeDenied
	}
	return s.checker.Check(ctx, s.domain, namespace, op)
}

// Get enforces the read guard before delegating to the underlying Driver.
func (s *scopedStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if err := s.checkAccess(ctx, namespace, CapOpRead); err != nil {
		return nil, err
	}
	return s.d.Get(ctx, namespace, key)
}

// Put routes through submitScoped so a cross-domain write is denied
// before it ever reaches the write queue (never merely rejected after
// being enqueued). The transaction body mirrors Driver.Put's own
// INSERT-ON-CONFLICT exactly (this package's single kv table, per
// driver.go's schemaDDL).
func (s *scopedStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	return s.d.exec.submitScoped(ctx, s.domain, namespace, s.checker, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)
			ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`, namespace, key, value)
		if err != nil {
			return wrapDBError(err, "sqlite: scoped put %s/%s", namespace, key)
		}
		return nil
	})
}

// Delete routes through submitScoped, exactly like Put. Deleting an
// absent key is not an error, matching Driver.Delete's own idempotent
// contract.
func (s *scopedStore) Delete(ctx context.Context, namespace, key string) error {
	return s.d.exec.submitScoped(ctx, s.domain, namespace, s.checker, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE namespace = ? AND key = ?`, namespace, key); err != nil {
			return wrapDBError(err, "sqlite: scoped delete %s/%s", namespace, key)
		}
		return nil
	})
}

// Scan enforces the read guard before delegating to the underlying
// Driver, so a cross-domain scan is refused before a single row is read —
// no partial-iteration leak window exists.
func (s *scopedStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	if err := s.checkAccess(ctx, namespace, CapOpRead); err != nil {
		return nil, err
	}
	return s.d.Scan(ctx, namespace, prefix)
}

// Tx wraps the underlying Driver's Tx, handing fn a scopedTx that applies
// the same per-call guard to every Get/Put/Delete/CompareAndSwap the
// closure makes, however many distinct namespaces it touches.
func (s *scopedStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	return s.d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, &scopedTx{tx: tx, domain: s.domain, checker: s.checker})
	})
}

// scopedTx is scopedStore.Tx's provider.Tx view: every method re-applies
// the same domain check scopedStore's top-level methods use, since a Tx
// closure can touch any namespace regardless of which domain's Store
// handle opened the transaction.
type scopedTx struct {
	tx      provider.Tx
	domain  string
	checker GrantChecker
}

var _ provider.Tx = (*scopedTx)(nil)

func (t *scopedTx) checkAccess(ctx context.Context, namespace string, op CapOp) error {
	if namespace == t.domain {
		return nil
	}
	if t.checker == nil {
		return ErrScopeDenied
	}
	return t.checker.Check(ctx, t.domain, namespace, op)
}

// Get enforces the read guard before delegating to the wrapped Tx.
func (t *scopedTx) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if err := t.checkAccess(ctx, namespace, CapOpRead); err != nil {
		return nil, err
	}
	return t.tx.Get(ctx, namespace, key)
}

// Put enforces the write guard before delegating to the wrapped Tx.
func (t *scopedTx) Put(ctx context.Context, namespace, key string, value []byte) error {
	if err := t.checkAccess(ctx, namespace, CapOpWrite); err != nil {
		return err
	}
	return t.tx.Put(ctx, namespace, key, value)
}

// Delete enforces the write guard before delegating to the wrapped Tx.
func (t *scopedTx) Delete(ctx context.Context, namespace, key string) error {
	if err := t.checkAccess(ctx, namespace, CapOpWrite); err != nil {
		return err
	}
	return t.tx.Delete(ctx, namespace, key)
}

// CompareAndSwap enforces the write guard before delegating to the
// wrapped Tx.
func (t *scopedTx) CompareAndSwap(ctx context.Context, namespace, key string, old, newValue []byte) error {
	if err := t.checkAccess(ctx, namespace, CapOpWrite); err != nil {
		return err
	}
	return t.tx.CompareAndSwap(ctx, namespace, key, old, newValue)
}
