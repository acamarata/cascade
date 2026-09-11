// Purpose: the elevated `backup restore` operation (05 §Epic S S-41.T4;
//
//	06 §5.14 — restore is elevated, verify is not): runs integrity.go's
//	gate FIRST and refuses on any gate failure — never a best-effort
//	restore — then fetches each selected domain's objects through
//	pipeline.go's Read (S-41.T1's inverse pipeline: age-decrypt ->
//	zstd-decompress -> chunk reassembly) and writes the recovered export
//	stream back through the Epic B storage layer (storage.Import,
//	B/S-03.T3) — the domain write-back's one production caller for both
//	SQLite and any future non-SQLite domain, per this ticket's journal
//	CONTRADICTION note: S-41.T1's capture already runs every domain
//	through storage.Export (never a raw SQLite file copy), so restore's
//	write-back is uniformly storage.Import, not two separate paths.
//
// Inputs: an ElevationProof (06 §5.14's operation-boundary evidence — this
//
//	ticket enforces its presence only; the real CLI/MCP attestation flow
//	is S-42.T3's), a RestoreOptions naming the Target, the destination
//	*sql.DB (every cascade.db domain lives in one shared kv table,
//	namespace-scoped — internal/storage/domains.go — so restore never
//	needs a per-domain DB handle), the manifest verification pubkey, and
//	an optional domain subset bounded by the manifest's own domain set.
//
// Outputs: a RestoreReport, or a typed fail-closed error with the
//
//	destination left exactly as it was found.
//
// Constraints: no storage.Import call happens before VerifyIntegrity
//
//	returns successfully — the fail-closed structural guarantee "a
//	refused restore leaves the destination untouched" rests on this
//	ordering, not on a rollback. Key custody: the backup age identity is
//	resolved via vault/env-ref only (integrity.go's AgeIdentity),
//	never caller-supplied, never persisted beside the target.
//
// SPORT: internal.backup.restore/ADDED (P1-E19-W4-S41-T4).

package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"sort"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrRestoreElevationRequired is Restore's refusal for a missing proof —
// 06 §5.14's elevated-verb class (`backup restore` ⚠), the source design's
// §23 "stronger authorization than read-only health queries."
var ErrRestoreElevationRequired = cascade.New(cascade.KindElevationRequired,
	"backup: restore requires a valid elevation attestation")

// RestoreOptions collects Restore's collaborators.
type RestoreOptions struct {
	Target Target
	// DB is the destination database restore writes into via
	// storage.Import. Every cascade.db domain lives in this one database
	// (namespace-scoped kv table), so one handle covers every domain.
	DB     *sql.DB
	PubKey ed25519.PublicKey
	// Domains restricts restore to this subset (`restore --domain`, 07),
	// bounded by the manifest's own domain set. Empty restores every
	// domain the manifest lists.
	Domains []string
	// ConflictStrategy is passed through to storage.Import for every
	// domain (default: storage.ConflictStrategyError, the zero value).
	ConflictStrategy storage.ConflictStrategy
}

// RestoreReport summarizes one successful Restore call.
type RestoreReport struct {
	Snapshot        SnapshotID
	Domains         []string
	RowsImported    int
	RowsSkipped     int
	RowsOverwritten int
}

// Restore runs the elevated restore operation: gate FIRST (integrity.go's
// VerifyIntegrity — refuses without writing anything on any failure), then
// domain selection bounded by the verified manifest, then per selected
// domain: fetch+decrypt+decompress+reassemble (pipeline.go's Read) and
// write back via storage.Import. No domain's Import runs until the gate
// has already passed for the whole snapshot.
func Restore(ctx context.Context, proof ElevationProof, opts RestoreOptions, id SnapshotID) (RestoreReport, error) {
	if proof == "" {
		return RestoreReport{}, ErrRestoreElevationRequired
	}
	if opts.DB == nil {
		return RestoreReport{}, cascade.New(cascade.KindInvalidInput,
			"backup: restore requires a non-nil destination database")
	}
	m, _, err := VerifyIntegrity(ctx, GateOptions{Target: opts.Target, PubKey: opts.PubKey}, id)
	if err != nil {
		return RestoreReport{}, err
	}
	selected, err := selectRestoreDomains(m.Domains, opts.Domains)
	if err != nil {
		return RestoreReport{}, err
	}
	identity, err := AgeIdentity()
	if err != nil {
		return RestoreReport{}, err
	}
	return restoreSelectedDomains(ctx, opts, identity, m, selected)
}

// restoreSelectedDomains runs restoreOneDomain for each selected domain in
// sorted order, accumulating one RestoreReport. Split out of Restore to
// keep Restore under the 50-line function cap.
func restoreSelectedDomains(ctx context.Context, opts RestoreOptions, identity string, m Manifest, selected []string) (RestoreReport, error) {
	report := RestoreReport{Snapshot: m.Snapshot}
	for _, domain := range selected {
		entry, ok := findManifestEntry(m, domain)
		if !ok {
			return RestoreReport{}, cascade.Newf(cascade.KindInternal,
				"backup: restore: domain %q selected but absent from its own verified manifest", domain)
		}
		imported, skipped, overwritten, err := restoreOneDomain(ctx, opts, identity, domain, entry)
		if err != nil {
			return RestoreReport{}, err
		}
		report.Domains = append(report.Domains, domain)
		report.RowsImported += imported
		report.RowsSkipped += skipped
		report.RowsOverwritten += overwritten
	}
	return report, nil
}

// restoreOneDomain reassembles one domain's export stream via
// Pipeline.Read (pipeline.go — age-decrypt, zstd-decompress, chunk
// reassembly, hash-verified per chunk) and applies it through
// storage.Import (B/S-03.T3). A decrypt/reassembly failure or an Import
// failure both refuse without this domain's rows landing: Import itself
// never commits a partial transaction (export_import.go's own atomicity
// guarantee), and no earlier domain in the loop is rolled back by this
// ticket — the gate having already passed means every domain's objects
// are known-good before any Import call starts.
func restoreOneDomain(ctx context.Context, opts RestoreOptions, identity, domain string, entry ManifestEntry) (imported, skipped, overwritten int, err error) {
	refs, err := manifestEntryObjectRefs(entry)
	if err != nil {
		return 0, 0, 0, err
	}
	data, err := (Pipeline{Target: opts.Target}).Read(ctx, identity, refs)
	if err != nil {
		return 0, 0, 0, err
	}
	rpt, err := storage.Import(ctx, opts.DB, storage.DomainID(domain), bytes.NewReader(data),
		storage.ImportOpts{ConflictStrategy: opts.ConflictStrategy})
	if err != nil {
		return 0, 0, 0, err
	}
	return rpt.RowsImported, rpt.RowsSkipped, rpt.RowsOverwritten, nil
}

// manifestEntryObjectRefs converts entry's hex-encoded refs back to
// pipeline.go's []ObjectRef, in stream order — reusing integrity.go's
// manifestRefToObjectRef rather than a second hex-decode implementation.
func manifestEntryObjectRefs(entry ManifestEntry) ([]ObjectRef, error) {
	refs := make([]ObjectRef, len(entry.Refs))
	for i, r := range entry.Refs {
		ref, err := manifestRefToObjectRef(r)
		if err != nil {
			return nil, err
		}
		refs[i] = ref
	}
	return refs, nil
}

// findManifestEntry returns m's entry for domain, if present.
func findManifestEntry(m Manifest, domain string) (ManifestEntry, bool) {
	for _, e := range m.Entries {
		if e.Domain == domain {
			return e, true
		}
	}
	return ManifestEntry{}, false
}

// selectRestoreDomains resolves the effective restore domain set: an
// empty requested slice restores every domain the verified manifest
// lists; a non-empty one is validated as a subset of it (`restore
// --domain`, bounded by the manifest's own domain set, 07) — a name
// outside the manifest refuses rather than silently being skipped.
// Returned sorted, so restore order is deterministic.
func selectRestoreDomains(manifestDomains, requested []string) ([]string, error) {
	if len(requested) == 0 {
		out := append([]string{}, manifestDomains...)
		sort.Strings(out)
		return out, nil
	}
	inManifest := make(map[string]bool, len(manifestDomains))
	for _, d := range manifestDomains {
		inManifest[d] = true
	}
	out := make([]string, 0, len(requested))
	for _, d := range requested {
		if !inManifest[d] {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"backup: restore: domain %q is not in this snapshot's manifest", d)
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}
