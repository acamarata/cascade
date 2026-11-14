// Package phantom provides handling for phantom replies in the GC system.
package phantom

// KindPhantom is the kind identifier for phantom replies.
const KindPhantom = "phantom"

// Reply represents a reply from the model.
type Reply struct {
	// Kind identifies the type of reply.
	Kind string
}

// Result holds the outcome of handling a reply.
type Result struct {
	// Retryable indicates whether the reply should be retried.
	Retryable bool
}

// SizeCache provides an interface for caching size limits.
type SizeCache interface {
	// MarkStale marks the cached size limit for the given model as stale.
	MarkStale(model string)
	// IsStale returns whether the cached size limit for the given model is stale.
	IsStale(model string) bool
	// Limit returns the cached size limit for the given model.
	Limit(model string) int
}

// HandleReply classifies a reply and updates the cache accordingly.
// A phantom reply is considered retryable and marks the cached size limit
// for that model as stale. It never lowers the cached limit automatically.
// A non-phantom reply is not handled and returns Retryable false.
func HandleReply(cache SizeCache, model string, reply Reply) Result {
	if reply.Kind == KindPhantom {
		cache.MarkStale(model)
		return Result{Retryable: true}
	}
	return Result{Retryable: false}
}
