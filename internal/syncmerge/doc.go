// Package syncmerge is a risk spike (P1-E13-W3-S27-T5, 12-QUALITY-CONSTITUTION.md
// Art.12) proving that Cascade's four sync-domain merge strategies converge
// under adversarial inputs before the production sync engine (Q/S-38.T2) is
// designed. The package is named syncmerge, not sync, so any file in it that
// needs a mutex can still import the stdlib "sync" package without a name
// collision (R-21.219).
//
// Every merge function in this package is guarded by the "spike" build tag
// and is therefore excluded from every release build on every GOOS target.
// This file is the sole untagged file, carrying only the package declaration
// and this doc comment, so that "go build ./internal/syncmerge/..." resolves
// the package with no logic included. See docs/adrs/ADR-sync-merge-semantics.md
// for the per-domain findings, and internal/syncmerge/testdata/fixtures for
// the adversarial fixture corpus. Q/S-38.T2 deletes every //go:build spike
// file in this package when its production engine lands.
package syncmerge
