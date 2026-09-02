// Package provider defines the public provider contracts that third-party
// code implements: agent, model, embedder, reranker, and review providers
// (added by other tickets), and the five storage families declared by
// P1-E02-W1-S02-T1:
//
//   - Store — the namespace-scoped key-value abstraction every cascade.db
//     domain (R-14.5: context, memory, audit, secrets, sessions, config,
//     retrieval, blobs, queue, jobs) and every plugin's namespaced storage
//     slot is built on (store.go).
//   - VectorStore — the R-14.4 canonical embedding-index surface (Upsert,
//     Query, Delete, Count, Namespaces), namespace-scoped throughout
//     (vector.go).
//   - BlobStore — content-addressed binary storage keyed by a BLAKE3-256
//     Hash (blob.go).
//   - Cache — an ephemeral, LRU-compatible, TTL-aware key/value store
//     (cache.go).
//   - Queue — an at-least-once work queue with visibility-timeout-based
//     redelivery (queue.go).
//
// Every driver that implements one of these five interfaces MUST return
// pkg/cascade taxonomy errors (*cascade.Error) at the interface boundary —
// never a raw fmt.Errorf/errors.New value; the boundary lint in
// internal/build enforces this. internal/storage/storetest provides a
// portable conformance suite (RunStoreTests, RunVectorStoreTests,
// RunBlobStoreTests, RunCacheTests, RunQueueTests): a driver that passes
// its family's suite function is correct by construction against this
// package's contracts.
//
// pkg/provider imports nothing from internal/ — it is the module's public
// SDK surface (12-QUALITY-CONSTITUTION.md Art.10.2), consumed by
// providers/** and plugins/** as well as by internal/** driver
// registrations.
package provider
