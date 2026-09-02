// Purpose: declare the VectorStore family contract — the R-14.4 canonical
//   surface (Upsert, Query, Delete, Count, Namespaces) every embedding-index
//   driver (localvector, pgvector, ...) implements.
// Inputs: a namespace on every call, plus vectors/query parameters.
// Outputs: matches ranked by similarity, or a pkg/cascade taxonomy error.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2);
//   method set is FROZEN verbatim by R-14.4 — B/S-03.T4 quotes the same
//   five names, so no method may be renamed, added, or removed here without
//   a T0 amendment to R-14.4.
// SPORT: pkg.provider.VectorStore/ADDED (P1-E02-W1-S02-T1).

package provider

import "context"

// VectorStore is the R-14.4 canonical vector-index contract: every
// operation is namespace-scoped via an explicit namespace argument, so one
// driver instance can back multiple independent indexes (per-plugin
// retrieval scopes, per-domain embeddings) without cross-contamination.
type VectorStore interface {
	// Upsert writes each Vector into namespace, creating or overwriting any
	// existing entry that shares its ID. Upsert reports
	// cascade.KindInvalidInput if any Vector's Values length does not match
	// the namespace's established dimensionality.
	Upsert(ctx context.Context, namespace string, vectors []Vector) error

	// Query returns the namespace's best matches for req, ranked by
	// descending similarity score, capped at req.TopK results.
	Query(ctx context.Context, namespace string, req VectorQuery) ([]VectorMatch, error)

	// Delete removes the vectors with the given IDs from namespace.
	// Deleting an absent ID is not an error.
	Delete(ctx context.Context, namespace string, ids []string) error

	// Count returns the number of vectors currently stored in namespace.
	Count(ctx context.Context, namespace string) (int, error)

	// Namespaces returns every namespace the driver currently has vectors
	// or an established dimensionality for.
	Namespaces(ctx context.Context) ([]string, error)
}

// Vector is one embedding entry: an identifier, its embedding values, and
// optional metadata carried alongside it for retrieval-time filtering and
// display.
type Vector struct {
	// ID uniquely identifies this vector within its namespace.
	ID string
	// Values is the embedding itself.
	Values []float32
	// Metadata is opaque, driver-preserved key/value data returned
	// alongside a VectorMatch for this ID. Values must be JSON-marshalable.
	Metadata map[string]any
}

// VectorQuery parameters one VectorStore.Query call.
type VectorQuery struct {
	// Values is the query embedding to rank namespace's vectors against.
	Values []float32
	// TopK caps the number of results returned. A driver MUST NOT return
	// more than TopK matches.
	TopK int
	// Filter restricts matches to vectors whose Metadata satisfies every
	// key/value pair given here (exact equality per key). A nil or empty
	// Filter applies no restriction.
	Filter map[string]any
}

// VectorMatch is one ranked result from VectorStore.Query.
type VectorMatch struct {
	// ID is the matched Vector's identifier.
	ID string
	// Score is the similarity score assigned by the driver; higher is more
	// similar. The scale is driver-defined (e.g. cosine similarity in
	// [-1, 1]) — callers compare scores only within one driver's results.
	Score float32
	// Metadata is the matched Vector's stored metadata.
	Metadata map[string]any
}
