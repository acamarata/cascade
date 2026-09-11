// Purpose: the pipeline orchestrator — chunk -> hash -> dedup ->
//
//	compress -> encrypt -> store (Write) and its verification-half
//	inverse, fetch -> decrypt -> decompress -> reassemble (Read) — plus
//	Snapshot, the single entry point that ties repo-config bootstrap and
//	one domain's capture to a Write call.
//
// Inputs: a Target, an age recipient string (Write/Snapshot) or identity
//
//	string (Read), and either a raw byte stream or a captured domain's
//	Exporter.
//
// Outputs: a WriteReport plus the ordered []ObjectRef a caller needs to
//
//	reconstruct the original stream, or the reconstructed bytes (Read).
//
// Constraints: Write never calls Encrypt with an empty recipient (fails
//
//	closed before touching Target at all); Read never returns a stream
//	whose recovered hash does not match its ObjectRef.
//
// SPORT: internal.backup.pipeline/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"bytes"
	"context"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ObjectRef is one stored chunk's identity and size, in stream order — the
// list a caller retains (S-41.T2's manifest is where it eventually lives)
// to reconstruct the original stream via Pipeline.Read.
type ObjectRef struct {
	Hash [32]byte
	Size int
}

// WriteReport summarizes one Pipeline.Write call.
type WriteReport struct {
	Chunks  int
	Stored  int // new objects actually written to Target
	Deduped int // chunks whose object id already existed, skipped
	Bytes   int // total plaintext bytes processed
}

// Pipeline ties a Target to the age recipient every Write call encrypts to.
type Pipeline struct {
	Target       Target
	AgeRecipient string
}

// Write runs the full forward engine over r: chunk, hash each chunk's
// plaintext (the dedup id), compress, encrypt, then Dedup stores only the
// chunks not already present. Compression and encryption run on every
// chunk before Dedup's presence check, trading CPU for a single shared
// "skip if present" implementation (Dedup) rather than duplicating that
// check here — see the ticket journal for this tradeoff. Bytes never
// touches Target unencrypted: an empty AgeRecipient refuses before
// chunking even begins.
func (p Pipeline) Write(ctx context.Context, r io.Reader) (WriteReport, []ObjectRef, error) {
	if p.AgeRecipient == "" {
		return WriteReport{}, nil, cascade.New(cascade.KindInvalidInput,
			"backup: pipeline requires an age recipient; refusing to write an unencrypted chunk")
	}
	chunks, err := ChunkStream(r)
	if err != nil {
		return WriteReport{}, nil, err
	}
	hashes, payloads, refs, err := transformChunks(chunks, p.AgeRecipient)
	if err != nil {
		return WriteReport{}, nil, err
	}
	results, err := Dedup(ctx, p.Target, hashes, payloads)
	if err != nil {
		return WriteReport{}, nil, err
	}
	report := WriteReport{Chunks: len(chunks)}
	for i, res := range results {
		report.Bytes += len(chunks[i].Data)
		if res.New {
			report.Stored++
		} else {
			report.Deduped++
		}
	}
	return report, refs, nil
}

// transformChunks compresses and encrypts every chunk up front, splitting
// this out of Write to keep Write under the 50-line function cap.
func transformChunks(chunks []Chunk, recipient string) (hashes [][32]byte, payloads [][]byte, refs []ObjectRef, err error) {
	hashes = make([][32]byte, len(chunks))
	payloads = make([][]byte, len(chunks))
	refs = make([]ObjectRef, len(chunks))
	for i, c := range chunks {
		hashes[i] = ObjectHash(c.Data)
		refs[i] = ObjectRef{Hash: hashes[i], Size: len(c.Data)}
		compressed, cErr := Compress(c.Data)
		if cErr != nil {
			return nil, nil, nil, cErr
		}
		encrypted, eErr := Encrypt(recipient, compressed)
		if eErr != nil {
			return nil, nil, nil, eErr
		}
		payloads[i] = encrypted
	}
	return hashes, payloads, refs, nil
}

// Read reverses Write: fetch each ref's stored object, decrypt, decompress,
// and reassemble the original byte stream in order. A missing object
// refuses (KindNotFound, from GetObject); a corrupted, truncated, or
// tampered object refuses (KindIntegrity, from Decrypt/Decompress) rather
// than returning a partial stream; a recovered chunk whose recomputed hash
// does not match its ObjectRef also refuses (KindIntegrity) — this is the
// pipeline's own verification half; restore ORCHESTRATION (domain
// selection, the integrity gate over a whole snapshot) is S-41.T4's.
func (p Pipeline) Read(ctx context.Context, identityStr string, refs []ObjectRef) ([]byte, error) {
	var out bytes.Buffer
	for _, ref := range refs {
		raw, err := readOneChunk(ctx, p.Target, identityStr, ref)
		if err != nil {
			return nil, err
		}
		out.Write(raw)
	}
	return out.Bytes(), nil
}

// readOneChunk fetches, decrypts, decompresses, and hash-verifies one
// ObjectRef, split out of Read to keep Read under the 50-line function cap.
func readOneChunk(ctx context.Context, t Target, identityStr string, ref ObjectRef) ([]byte, error) {
	encrypted, err := GetObject(ctx, t, ref.Hash)
	if err != nil {
		return nil, err
	}
	compressed, err := Decrypt(identityStr, encrypted)
	if err != nil {
		return nil, err
	}
	raw, err := Decompress(compressed)
	if err != nil {
		return nil, err
	}
	if ObjectHash(raw) != ref.Hash {
		return nil, cascade.New(cascade.KindIntegrity, "backup: recovered chunk hash mismatch")
	}
	return raw, nil
}

// Snapshot is the engine's single entry point: it opens (or, on first
// call, initializes) t's repo config, then captures and writes exp's
// domain through Pipeline.Write. S-41.T2's snapshot/manifest layer is the
// intended production caller (see internal/build/testonly-allow.json for
// this symbol's wiring exemption).
func Snapshot(ctx context.Context, t Target, recipient string, exp Exporter) (WriteReport, []ObjectRef, error) {
	if err := ensureRepoConfig(ctx, t, recipient); err != nil {
		return WriteReport{}, nil, err
	}
	rc, err := exp.Export(ctx)
	if err != nil {
		return WriteReport{}, nil, err
	}
	defer func() { _ = rc.Close() }()
	return Pipeline{Target: t, AgeRecipient: recipient}.Write(ctx, rc)
}

// ensureRepoConfig reads t's repo config, initializing it with recipient on
// first use (KindNotFound) and refusing (without touching Target further)
// when an existing config names a different recipient than the caller
// supplied — Snapshot never silently re-encrypts a repo to a new key.
func ensureRepoConfig(ctx context.Context, t Target, recipient string) error {
	cfg, err := ReadRepoConfig(ctx, t)
	if err != nil {
		if !cascade.HasKind(err, cascade.KindNotFound) {
			return err
		}
		return WriteRepoConfig(ctx, t, RepoConfig{LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient})
	}
	if cfg.AgeRecipient != recipient {
		return cascade.New(cascade.KindConflict, "backup: repo is already initialized to a different age recipient")
	}
	return nil
}
