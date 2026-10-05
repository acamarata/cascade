// Purpose: the harvested-record model: canonical JSON, content-addressed
// fixture names, the four owning output dirs, and scratch v1-home staging.
// Inputs: redacted record maps and verified input files.
// Outputs: fixture values (dir, name, bytes, provenance row) and staged
// scratch files whose mtime is a fixed instant, never the copy time.
// Constraints: canonical JSON is sorted keys, 2-space indent, LF, one
// trailing newline; a fixture name is <prefix>-<first 16 hex of sha256 of
// those bytes>.json. Scratch lives under os.MkdirTemp and is removed by the
// caller; nothing here writes outside it.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

// formatVersion is the harvester format version stamped into every record
// and provenance row. Bump it when the record shape changes.
const formatVersion = "1"

// stagedSourceTime is the mtime every staged input carries. The v1 memory
// importer preserves source mtimes into the v2 file, so a copy-time mtime
// would make every harvest produce new bytes.
var stagedSourceTime = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

// outputDirs maps a domain to its owning package's fixture dir, relative to
// the module root.
var outputDirs = map[v1.Domain]string{
	v1.DomainMemory:   "internal/memory/testdata/v1-goldens/migration",
	v1.DomainVault:    "internal/secrets/testdata/v1-goldens/migration",
	v1.DomainAccounts: "internal/providers/registry/testdata/v1-goldens/migration",
	v1.DomainConfig:   "internal/runtime/testdata/v1-goldens/migration",
}

// pinnedInputDir is the pinned input dir, relative to the module root.
const pinnedInputDir = "internal/migration/v1/testdata/v1-goldens"

// provenanceRow is one README provenance table row.
type provenanceRow struct {
	Domain   string
	Input    string
	Digest   string
	Format   string
	Captured string
}

// key is the row's identity: (domain, input digest, format version).
func (p provenanceRow) key() string {
	return p.Domain + "\x00" + p.Digest + "\x00" + p.Format
}

// fixture is one harvested file ready to write.
type fixture struct {
	Domain v1.Domain
	Name   string
	Data   []byte
	Row    provenanceRow
}

// newFixture canonicalises record and names it by content.
func newFixture(domain v1.Domain, prefix string, input inputFile, record map[string]any) (fixture, error) {
	record["domain"] = string(domain)
	record["input"] = input.Rel
	record["input_digest"] = input.Digest
	record["format_version"] = formatVersion
	data, err := canonicalJSON(record)
	if err != nil {
		return fixture{}, err
	}
	sum := sha256.Sum256(data)
	return fixture{
		Domain: domain,
		Name:   prefix + "-" + hex.EncodeToString(sum[:])[:16] + ".json",
		Data:   data,
		Row:    provenanceRow{Domain: string(domain), Input: input.Rel, Digest: input.Digest, Format: formatVersion},
	}, nil
}

// canonicalJSON encodes v with sorted keys, 2-space indent, no HTML
// escaping and exactly one trailing LF.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "golden harvest: encode record")
	}
	return buf.Bytes(), nil
}

// scratchParent is where scratch dirs are created; "" means os.TempDir().
// Tests point it at t.TempDir().
var scratchParent = ""

// newScratch creates one private scratch dir.
func newScratch() (string, error) {
	dir, err := os.MkdirTemp(scratchParent, "cascade-golden-*")
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: create scratch dir")
	}
	return dir, nil
}

// stage writes data to root/rel (slash path) with owner-only permissions
// and the fixed stagedSourceTime mtime.
func stage(root, rel string, data []byte) error {
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: create scratch v1 home")
	}
	if err := writeScratch(target, data); err != nil {
		return err
	}
	if err := os.Chtimes(target, stagedSourceTime, stagedSourceTime); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: pin staged mtime")
	}
	return nil
}

// writeScratch creates a new scratch file. Scratch is private, throwaway and
// never published, so it is written in place rather than atomically.
func writeScratch(target string, data []byte) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path built inside a fresh scratch dir
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: stage input")
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return cascade.New(cascade.KindUnavailable, "golden harvest: stage input write failed")
	}
	return nil
}
