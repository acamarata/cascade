// Package inventory computes the counts the owner asked for: "for things
// like number of something ... we need a good SPORT model or where to see
// as we develop because this changes all the time." It answers "how many"
// for the facts this tree states in prose in a dozen places (error kinds,
// storage domains, CLI commands, providers, plugins, supported platforms,
// SPORT-tagged lines) from the SAME artifact the program itself uses, not
// from a second hand-maintained list.
//
// Two kinds of count live here, and they are never confused (merge.go
// keeps them apart):
//
//   - Live (live.go): ErrorKinds, StorageDomains, CLICommands. Computed
//     fresh on every call from in-process data (pkg/cascade.AllKinds,
//     internal/storage.AllDomains, the real cobra tree). These can never
//     drift from reality because there is no snapshot step between the
//     source changing and the count reflecting it.
//   - Generated (tree.go, embed.go): Providers, Plugins, SPORTLines,
//     Platforms. An installed binary has no providers/, plugins/, or
//     .github/workflows/ directory on disk, so these are computed once
//     against a real checkout by internal/inventory/gen and baked into the
//     tracked counts.json this package embeds. They can drift between a
//     tree change and the next `go run ./internal/inventory/gen` —
//     internal/build's counts drift gate (countsdriftgate.go) is what
//     catches that gap, by recomputing the same functions against the live
//     tree and failing when they disagree with the tracked artifact.
//
// What this package does NOT do: SPORTLines counts comment LINES, not
// distinct entities. A single "// SPORT: a/ADDED, b/ADDED, c/ADDED" line
// carrying three dotted paths counts once, and the same entity re-stated
// across two files' SPORT lines is not deduplicated. It is a drift signal
// ("did the marker count change"), not the queryable entity registry the
// owner also asked for — that is future work, out of this ticket's scope,
// and is stated here rather than implied by a misleadingly precise number.
package inventory
