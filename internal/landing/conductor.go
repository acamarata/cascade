package landing

import "context"

// Conductor lands a single item.
type Conductor interface {
	// Land lands item and returns an error when it cannot.
	Land(ctx context.Context, item Item) error
}
