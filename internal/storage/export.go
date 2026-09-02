package storage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"time"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: per-domain JSON export — the data-portability building block
//
//	B/S-03.T3 assigns as the internal backup/parity layer (backup UX and
//	CLI commands arrive in later epics; see docs_updates below for what
//	is, and is not, reachable today). This file holds the wire types
//	shared with export_import.go plus Export itself; export_kv.go holds
//	the raw-SQL helpers both files share, export_clock.go the clock
//	injection seam, split under R-14.117 (Art.10.3's 300-line cap).
//
// Inputs: an open *sql.DB against a cascade.db-shaped database (ideally
//
//	Bootstrap has already run against it — see readSchemaVersion's doc for
//	what happens when it has not) and a DomainID from the closed R-14.5
//	set.
//
// Outputs: a line-delimited JSON stream on w: one header line, then one
//
//	line per row currently stored under that domain's namespace in the
//	shared kv table (providers/sqlite/driver.go's schemaDDL — see
//	"What Export actually reads" below).
//
// Constraints: Art.5 — no platform-specific imports; GOOS=linux and
//
//	GOOS=windows both build clean. Art.7.3/forbidigo — Export never reads
//	the wall clock directly (export_clock.go). Determinism — see Export's
//	own doc comment for the full contract.
//
// What Export actually reads: B/S-03.T1's Bootstrap creates only a
// per-domain ANCHOR table (domains.go's domainRootTable, schema
// `(id INTEGER PRIMARY KEY)`, no business columns — retention.go's package
// doc already establishes this for the identical reason). The one
// concrete, already-landed row-level store scoped by domain is
// providers/sqlite's shared `kv` table (namespace, key, value), landed by
// B/S-02.T2 and depended on transitively via S-03.T1 exactly as this
// ticket's full_desc names it ("the write-executor from B/S-02.T2"). This
// ticket's files_scope forbids editing providers/sqlite (same constraint
// retention.go documents), so export.go/export_kv.go duplicate the kv
// table's shape by convention rather than importing it — the identical
// shape-only-agreement pattern domains.go's bootstrapLedgerTable and
// health.go's mainDBFilePath already establish in this package for the
// applied_migrations ledger. Exporting a domain therefore means: every
// (key, value) pair currently stored under namespace = string(domain) in
// that shared table, not the (data-free) anchor table.
//
// SPORT: internal.storage.export.Export/ADDED,
//
//	internal.storage.export.Import/ADDED,
//	internal.storage.export.ImportOpts/ADDED,
//	internal.storage.export.ImportVersionError/ADDED
//	(P1-E02-W1-S03-T3).

// recordTypeHeader and recordTypeRow are the "_type" discriminator values
// every line in an export stream carries — the first line is always
// recordTypeHeader, every subsequent line recordTypeRow. Kept as named
// constants (never inlined string literals) so export.go and
// export_import.go agree by construction, not convention.
const (
	recordTypeHeader = "header"
	recordTypeRow    = "row"
)

// ExportHeader is the export stream's mandatory first line. Field order
// here is NOT the general "alphabetical within the row struct" determinism
// rule (that applies to row records only, per full_desc) — it is the
// literal, contract-fixed header shape from the ticket's own worked
// example, and encoding/json marshals struct fields in declaration order,
// which is what keeps this the wire order.
//
// Format stability: this struct (plus exportRow) IS the export line
// format's version identity, in the same sense internal/output.Envelope's
// field set and order define EnvelopeVersion's wire shape — there is no
// separate numeric format-version field in the header line, because the
// worked example in this ticket's own contract fixes the header's shape
// exactly as five fields, in this order, with no room for a sixth. A
// future incompatible line-shape change (a field removed, renamed,
// retyped, or a new record type/field a reader must understand to parse
// correctly) is exactly the kind of change EnvelopeVersion exists to
// signal on that other wire format, and would need the equivalent here:
// add an explicit version field to ExportHeader at that time (a purely
// additive change to THIS struct, not a retroactive one) and bump it on
// every such change from then on. An additive, optional field never
// requires that. SchemaVersion below is a distinct concept — it names the
// DATA's cascade.db schema generation, never the container format's.
type ExportHeader struct {
	// Type is always recordTypeHeader ("header").
	Type string `json:"_type"`
	// Domain is the exported domain's string form. Import compares this
	// against its own domain argument (never the file's namespace column,
	// which is not even part of the wire format) to refuse a cross-domain
	// import — see Import's doc comment.
	Domain string `json:"domain"`
	// SchemaVersion is the source database's on-disk schema_version stamp
	// (domains.go's bootstrapSchemaVersion / health.go's
	// checkSchemaVersion) at the moment Export ran — the DATA's schema
	// generation, not the container format's (see the "Format stability"
	// note on this struct's own doc comment above).
	SchemaVersion int `json:"schema_version"`
	// ExportedAt is the capture instant, RFC3339 in UTC. Sourced from
	// exportClock (export_clock.go), never a bare time.Now.
	ExportedAt string `json:"exported_at"`
	// CascadeVersion is the ldflags-stamped build identity
	// (internal/buildinfo.Version) of the binary that ran Export ("dev"
	// for an unstamped local build — see internal/buildinfo's own doc).
	CascadeVersion string `json:"cascade_version"`
}

// exportRow is one data line. Field order is alphabetical by JSON key name
// ("_type" < "key" < "value", since '_' sorts before any lowercase ASCII
// letter) per the determinism contract — Type, Key, Value in that
// declaration order satisfies it while also reading naturally. Value is
// the row's raw bytes, base64-standard-encoded: the kv table's value
// column is an arbitrary BLOB (never guaranteed valid UTF-8), and JSON
// strings must be valid UTF-8, so base64 is the only lossless wire
// encoding available without inventing a second, conditional shape.
//
// Two determinism-contract clauses in full_desc do not apply to this
// table's actual two-column (key, value) shape and are satisfied
// vacuously, stated here rather than left for a reviewer to wonder about:
// (a) "float values use strconv.FormatFloat" — neither column is numeric;
// Export/Import never interpret value's bytes, so no float formatting
// path exists to be non-deterministic. (b) "NULL columns encode as JSON
// null" — the kv table's value column is `BLOB NOT NULL` (driver.go's
// schemaDDL); no row this format ever reads or writes can be NULL.
type exportRow struct {
	Type  string `json:"_type"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// newExportRow builds the wire row for one (key, value) pair, base64-
// encoding value.
func newExportRow(key string, value []byte) exportRow {
	return exportRow{Type: recordTypeRow, Key: key, Value: base64.StdEncoding.EncodeToString(value)}
}

// decodedValue base64-decodes r.Value, wrapped as a taxonomy KindIntegrity
// error on failure (a corrupt/hand-edited export file, not a caller
// mistake) — Import's atomic-rollback tests exercise this path via a
// mid-stream line whose "value" field is not valid base64.
func (r exportRow) decodedValue() ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(r.Value)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, err, "storage: import decode row value for key %q", r.Key)
	}
	return b, nil
}

// Export serializes every (key, value) pair currently stored under
// domain's namespace in the shared kv table to w as a deterministic,
// line-delimited JSON stream: one header line (ExportHeader), then one
// exportRow line per row, ordered by key ascending (the kv table's own
// PRIMARY KEY (namespace, key) order — see driver.go's schemaDDL — so
// "ordered by primary key" is satisfied by the query's ORDER BY matching
// the table's declared key order, not by an incidental sort).
//
// Determinism: given an unchanged database state and an unchanged
// exportClock reading, two Export calls produce byte-for-byte identical
// output — see export_clock.go for why exported_at does not defeat this
// in tests (TestExportDeterminism freezes the clock; production callers
// necessarily see a different exported_at per real call, which is exactly
// what a capture timestamp is for and is not a determinism violation of
// the DATA the ticket's contract actually cares about matching byte-for-
// byte, only of the one field that is a timestamp by definition).
//
// Export opens db.BeginTx as a plain (non-exclusive) transaction and only
// ever issues reads through it — WAL mode (domains.go's Bootstrap sets it)
// lets this run concurrently with writes through the single write
// connection without blocking either side, giving Export a consistent
// snapshot of domain's rows as of the moment the transaction opened.
//
// A domain argument outside the closed R-14.5 set (including
// ReservedPluginHostNamespace, R-14.100) is refused with a KindInvalidInput
// error before any query runs. A domain whose kv rows have never been
// written (or whose kv table does not exist at all yet) is not an error:
// Export still writes the header line and simply emits zero row lines —
// this is domainless_test.go/roundtrip's "empty domain" case.
func Export(ctx context.Context, db *sql.DB, domain DomainID, w io.Writer) error {
	if !validDomain(domain) {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: export refused: domain %q is not a member of the closed R-14.5 domain set", domain)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "storage: export begin read transaction")
	}
	defer func() { _ = tx.Rollback() }()

	schemaVersion, err := readSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}

	header := ExportHeader{
		Type:           recordTypeHeader,
		Domain:         string(domain),
		SchemaVersion:  schemaVersion,
		ExportedAt:     exportClock.Now().UTC().Format(time.RFC3339),
		CascadeVersion: buildinfo.Version,
	}
	if err := writeJSONLine(w, header); err != nil {
		return err
	}

	return exportDomainRows(ctx, tx, domain, w)
}

// exportDomainRows streams every kv row for domain, in key order, as
// exportRow lines. Split from Export to keep Export itself well under
// Art.10.3's 50-line function cap.
func exportDomainRows(ctx context.Context, tx *sql.Tx, domain DomainID, w io.Writer) error {
	exists, err := kvTableExists(ctx, tx)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: export check kv table for domain %s", domain)
	}
	if !exists {
		return nil // never-written domain: header only, zero row lines.
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT key, value FROM `+quoteIdent(exportKVTable)+` WHERE namespace = ? ORDER BY key`,
		string(domain),
	)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: export query kv rows for domain %s", domain)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "storage: export scan kv row for domain %s", domain)
		}
		if err := writeJSONLine(w, newExportRow(key, value)); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "storage: export iterate kv rows for domain %s", domain)
	}
	return nil
}

// writeJSONLine marshals v as compact JSON and writes it followed by a
// single "\n" (never "\r\n" — platform-agnostic per Art.5, and required
// for byte-for-byte determinism across platforms). Mirrors
// internal/output.NDJSONWriter.Emit's convention exactly (one compact JSON
// value per line), duplicated rather than imported: internal/output is a
// CLI-presentation package this internal-API ticket has no reason to
// depend on.
func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "storage: marshal export json line")
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "storage: write export stream")
	}
	return nil
}
