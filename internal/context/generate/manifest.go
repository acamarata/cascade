package generate

// Purpose: the generation manifest, .cascade/generation-manifest.json. It
//   records, per generated file, the whole-file hash, the hash of every
//   managed block and the hash of everything outside the blocks, so the
//   Writer can tell the last generated base from a hand edit and Check can
//   tell a hand-edited block from a hand-edited bystander.
// Inputs: a repository root and a Manifest.
// Outputs: canonical bytes (entries by path, blocks by id, deferrals by
//   generator, 2-space indent, LF) or a decoded, validated Manifest.
// Constraints: ReadManifest refuses an unknown field, trailing data, a
//   version other than 1 and malformed hashes with KindInvalidInput.
//   Equal input writes equal bytes: the only clock value is GeneratedAt,
//   which the caller injects. Writes go through runtime.WriteFileAtomic.
// SPORT: context-engine/generation-manifest (ADD, P1-GEN-05).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ManifestRel is the manifest's repo-relative path. .cascade/ is ignored
// by the repository's .gitignore, so the manifest is never tracked.
const ManifestRel = ".cascade/generation-manifest.json"

// ManifestVersion is the only schema version this package reads or writes.
const ManifestVersion = 1

// Manifest is the on-disk record of the last generation.
type Manifest struct {
	Version     int        `json:"version"`
	GeneratedAt string     `json:"generated_at"` // RFC3339 UTC, injected clock
	Entries     []Entry    `json:"entries"`
	Deferrals   []Deferral `json:"deferrals,omitempty"`
}

// Entry records one generated file.
type Entry struct {
	Path          string      `json:"path"` // slash, repo-relative
	GeneratorID   string      `json:"generator_id"`
	FileSHA256    string      `json:"file_sha256"`
	OutsideSHA256 string      `json:"outside_sha256"` // the file with every recorded block's lines removed
	Blocks        []BlockHash `json:"blocks"`
}

// BlockHash records one managed block's body hash and owning generator.
type BlockHash struct {
	ID            string `json:"id"`
	GeneratorID   string `json:"generator_id"`
	ContentSHA256 string `json:"content_sha256"`
}

// Deferral records a generator that declined to run. P1-GEN-09 writes it.
type Deferral struct {
	Generator string `json:"generator"`
	Result    string `json:"result"`
	Reason    string `json:"reason"`
	Owner     string `json:"owner"`
	Expiry    string `json:"expiry"`
}

// hexSHA is the lowercase hex shape of every recorded hash.
var hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sha256Hex hashes b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ReadManifest decodes the manifest under repoRoot. An absent file is
// (zero, false, nil). The path is confined like every other: a symlinked
// .cascade is refused.
func ReadManifest(repoRoot string) (Manifest, bool, error) {
	c, err := openConfined(repoRoot, ManifestRel)
	if err != nil {
		return Manifest{}, false, err
	}
	defer c.close()
	data, ok, err := c.read()
	if err != nil || !ok {
		return Manifest{}, false, err
	}
	m, err := decodeManifest(data)
	if err != nil {
		return Manifest{}, false, err
	}
	return m, true, nil
}

// decodeManifest parses and validates manifest bytes.
func decodeManifest(data []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, cascade.Wrap(cascade.KindInvalidInput, err, "generate: decode manifest")
	}
	if _, err := dec.Token(); err != io.EOF {
		return Manifest{}, cascade.New(cascade.KindInvalidInput, "generate: trailing data after manifest")
	}
	if m.Version != ManifestVersion {
		return Manifest{}, cascade.Newf(cascade.KindInvalidInput, "generate: manifest version %d, want %d", m.Version, ManifestVersion)
	}
	return m, validateManifest(m)
}

// validateManifest refuses a manifest the Writer could not have produced.
func validateManifest(m Manifest) error {
	if m.GeneratedAt != "" {
		if _, err := time.Parse(time.RFC3339, m.GeneratedAt); err != nil {
			return cascade.Wrap(cascade.KindInvalidInput, err, "generate: manifest generated_at")
		}
	}
	paths := map[string]bool{}
	for _, e := range m.Entries {
		if err := checkRel(e.Path); err != nil {
			return err
		}
		if paths[e.Path] {
			return cascade.Newf(cascade.KindInvalidInput, "generate: duplicate manifest entry %s", e.Path)
		}
		paths[e.Path] = true
		if err := validateEntry(e); err != nil {
			return err
		}
	}
	return nil
}

// validateEntry checks one entry's generator, hashes and blocks.
func validateEntry(e Entry) error {
	if e.GeneratorID == "" || !hexSHA.MatchString(e.FileSHA256) || !hexSHA.MatchString(e.OutsideSHA256) {
		return cascade.Newf(cascade.KindInvalidInput, "generate: manifest entry %s has an empty generator or malformed hash", e.Path)
	}
	ids := map[string]bool{}
	for _, b := range e.Blocks {
		if !blockIDPattern.MatchString(b.ID) || ids[b.ID] || b.GeneratorID == "" || !hexSHA.MatchString(b.ContentSHA256) {
			return cascade.Newf(cascade.KindInvalidInput, "generate: manifest entry %s has a bad block %q", e.Path, b.ID)
		}
		ids[b.ID] = true
	}
	return nil
}

// canonical returns a sorted, nil-free copy of m with the current version.
func canonical(m Manifest) Manifest {
	out := Manifest{Version: ManifestVersion, GeneratedAt: m.GeneratedAt, Entries: []Entry{}}
	for _, e := range m.Entries {
		e.Blocks = append([]BlockHash{}, e.Blocks...)
		sort.Slice(e.Blocks, func(i, j int) bool { return e.Blocks[i].ID < e.Blocks[j].ID })
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Path < out.Entries[j].Path })
	if len(m.Deferrals) > 0 {
		out.Deferrals = append([]Deferral{}, m.Deferrals...)
		sort.Slice(out.Deferrals, func(i, j int) bool { return out.Deferrals[i].Generator < out.Deferrals[j].Generator })
	}
	return out
}

// encodeManifest renders the canonical bytes of m.
func encodeManifest(m Manifest) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(canonical(m)); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "generate: encode manifest")
	}
	return b.Bytes(), nil
}

// WriteManifest publishes m canonically: entries by path, blocks by id,
// deferrals by generator, 2-space indent, LF, mode 0644. A manifest
// ReadManifest would refuse is never written.
func WriteManifest(repoRoot string, m Manifest) error {
	m = canonical(m)
	if err := validateManifest(m); err != nil {
		return err
	}
	data, err := encodeManifest(m)
	if err != nil {
		return err
	}
	c, err := openConfined(repoRoot, ManifestRel)
	if err != nil {
		return err
	}
	defer c.close()
	if err := c.ensureParent(); err != nil {
		return err
	}
	return runtime.WriteFileAtomic(c.abs, data, 0o644)
}

// entryFor builds the manifest entry for content. owners maps a block id
// to its generator; an id not in the map belongs to generatorID.
func entryFor(path, generatorID string, content []byte, form MarkerForm, owners map[string]string) (Entry, error) {
	spans, err := scanBlocks(content, form, nil)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Path: path, GeneratorID: generatorID, FileSHA256: sha256Hex(content),
		OutsideSHA256: sha256Hex(outsideBytes(content, spans)), Blocks: []BlockHash{}}
	for _, sp := range spans {
		owner := generatorID
		if o, ok := owners[sp.id]; ok {
			owner = o
		}
		e.Blocks = append(e.Blocks, BlockHash{ID: sp.id, GeneratorID: owner, ContentSHA256: sha256Hex(content[sp.bodyStart:sp.bodyEnd])})
	}
	return e, nil
}
