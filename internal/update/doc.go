// Package update is `cascade self-update` as separate callable steps:
// ResolveChannel, ReadReceipt/WriteReceipt, LatestStable, FetchVerified
// (over the one minisign verifier), Stage, Swap, Restore, Drain,
// WaitVersion, Check and Apply. Each step is usable on its own, so a later
// update surface can drive them individually.
//
// SPORT: internal/update/ADDED.
package update
