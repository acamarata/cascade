// Package memory provides the memory primitives and the consolidation
// ladder, on a store in which the files ARE the memory.
//
// # The files are the source of truth
//
// A memory record is one markdown file at {base}/{kind}/{name}.md: a
// frontmatter header carrying every field except the body, then the body.
// A deletion is a {name}.md.tombstone marker beside the record it retires.
// That tree is the whole of the store's state. It can be read, edited,
// diffed and version-controlled with ordinary tools, which is the point of
// keeping it in files rather than in a database.
//
// ProjectionJob projects this tree into an index for search. That
// projection is derived state and nothing else: it can be deleted and
// rebuilt from the tree alone, and nothing it holds is needed to
// reconstruct a record. When the two disagree, THE FILE IS AUTHORITATIVE
// and the projection is stale; the response is to rebuild the projection,
// never to write the file back from it. A tombstone is part of that
// contract rather than an implementation detail: it is how a projection
// learns of a deletion by scanning, without needing a previous listing to
// diff against.
//
// # The projection cannot widen what a query may see
//
// A record the store refuses to read is refused by the projection too, and
// any row it previously had is withdrawn, so nothing unreadable, retired
// or expired can be reached through the index that could not be reached
// through the store. Refusals are reported rather than silent: one damaged
// file costs exactly its own row and is named in the run's result, in the
// same spirit as a listing that parses nothing so one bad file cannot make
// a kind unlistable.
//
// # Drift is expected, not corruption
//
// Because a user may edit a record with any editor, the body on disk moves
// on without the store being told. Provenance.ContentHash is therefore the
// body's digest AS OF THE LAST STORE WRITE, and MemoryEntry.BodyHash is
// the digest of the body in hand. A difference between them means the file
// was edited outside the store, which is a normal event and the signal a
// projection uses to decide what to re-derive. It is not an integrity
// failure and reads do not refuse it.
//
// # Reads fail closed
//
// A record that cannot be parsed whole is refused whole. No read returns a
// half-populated record, and no damaged file makes the rest of the store
// unreadable: listing reads directory names and parses nothing, so one bad
// file affects exactly one record. A record declaring a format version
// this build does not know is refused as unsupported rather than read on a
// best-effort basis, and a write will not overwrite a file it could not
// read, because the file may have been written by a newer build.
//
// # Writes do not lose data on interruption
//
// Every write goes to a temporary file in the target's own directory and
// is flushed before being renamed into place, so an interruption leaves
// either the previous record or nothing new, never a truncated one. A
// delete writes the tombstone before removing the record, so an
// interruption between the two steps leaves both files, and a tombstone
// always wins over the record beside it.
//
// # Determinism
//
// Timestamps come from an injected Clock and are canonicalized to UTC.
// Encoding writes a fixed key order with no map iteration reaching the
// file, so identical records produce identical bytes on any machine.
// Listings are lexically ordered. The content hash is BLAKE3, pure Go, and
// depends on nothing but the bytes it is given.
package memory
