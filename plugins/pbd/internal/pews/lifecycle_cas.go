// Package pews (lifecycle_cas.go): CASAppender is the compare-and-append
// seam a JournalStore optionally implements so applyEvent (lifecycle_apply.go)
// can make a lifecycle claim atomic across goroutines and processes, not
// just serialized in-process (lifecycle.go's plain Append never checked a
// sequence, so two concurrent claims could both succeed — see this
// ticket's journal). FileJournalStore (plugins/pbd/lifecycle_appendif.go)
// is the shipped implementation, holding an exclusive advisory lock across
// load, sequence check and save.
// SPORT: plugins/pbd/internal/pews lifecycle_cas (ADD) — P1-PBD-07.
package pews

import (
	"context"
	"encoding/json"
)

// CASAppender is JournalStore's optional compare-and-append extension.
// AppendIf refuses KindConflict when entityID's live entry count does not
// equal expectedSeq, and returns the existing entry (appending nothing) for
// a repeated operationID rather than appending a duplicate. A store that
// cannot offer this guarantee simply does not implement CASAppender; a
// caller that requires it type-asserts and refuses KindUnsupported when the
// assertion fails, rather than silently falling back to plain Append.
type CASAppender interface {
	AppendIf(ctx context.Context, entityID string, expectedSeq int, event LifecycleEvent, operationID string, payload json.RawMessage) (JournalEntry, error)
}
