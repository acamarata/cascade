package landing

import "context"

// Journal records which items of a batch have landed so a resumed run
// can skip them.
type Journal interface {
	// Record marks itemKey in batchID as landed.
	Record(ctx context.Context, batchID string, itemKey string) error
	// Has reports whether itemKey in batchID was recorded as landed.
	Has(ctx context.Context, batchID string, itemKey string) (bool, error)
}
