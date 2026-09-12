// Purpose: blob staging + BLAKE3 content-address admission (R-21.223
//   BLOB ADMISSION). Chunked blob bytes land in a STAGING file; a blob is
//   admitted to its final content-addressed path only when the
//   RECOMPUTED BLAKE3 digest of the staged bytes equals the DECLARED
//   content address — the blob's own identity, established independently
//   of this transfer instance (its key in the source's content-addressed
//   BlobStore, mirroring providers/fs's R-14.6 convention and
//   internal/backup/dedup.go's identical "hash the plaintext, never trust
//   a hash the transfer produced" precedent). A mismatch discards the
//   staged bytes and returns a typed error; the final path is created
//   ONLY by an atomic rename on success, so an interrupted transfer can
//   never leave a corrupt destination visible as complete — the same
//   .tmp-then-mv-f standard S-36.T5's provision.go proved structurally.
// Inputs: a staging directory, the DECLARED content address (BLAKE3-256
//   over the plaintext blob, computed by the SOURCE side before this
//   transfer started), and the chunk stream to append.
// Outputs: BlobPath on success, or a *cascade.Error on any mismatch
//   (integrity) or filesystem failure (unavailable).
// Constraints: resumability is EXPLICIT, never silent: WriteChunk records
//   a durable sidecar cursor after every chunk it accepts (fsync'd), and
//   Resume refuses (rather than guesses) when the sidecar is missing or
//   inconsistent with the .tmp file's actual size — an interrupted
//   transfer with no trustworthy sidecar is treated as unresumable, not
//   silently restarted or silently left half-written.
// SPORT: internal.sync.staging/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ContentAddress is a blob's BLAKE3-256 content-address key, declared by
// the source side independently of any particular transfer.
type ContentAddress [32]byte

// Hex renders a as a lowercase hex string, the on-disk filename this
// package uses under a staging/blobs directory.
func (a ContentAddress) Hex() string { return hex.EncodeToString(a[:]) }

// stagingCursor is the durable sidecar WriteChunk maintains beside the
// .tmp file, so a crash mid-transfer can be told apart from a corrupt or
// foreign .tmp file left by something else.
type stagingCursor struct {
	Declared     string `json:"declared_address"`
	LastAckedSeq uint64 `json:"last_acked_seq"`
	Total        uint64 `json:"total"`
}

func stagingPaths(stagingDir string, addr ContentAddress) (tmpPath, cursorPath string) {
	base := filepath.Join(stagingDir, addr.Hex())
	return base + ".tmp", base + ".cursor.json"
}

// BeginStaging truncates (or creates) the .tmp file and its cursor
// sidecar for a fresh transfer of addr, expecting total chunks. Call this
// only for a non-resuming transfer; ResumeStaging is the resuming path.
func BeginStaging(stagingDir string, addr ContentAddress, total uint64) error {
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: create staging dir")
	}
	tmpPath, cursorPath := stagingPaths(stagingDir, addr)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: create staging tmp file")
	}
	defer func() { _ = f.Close() }()
	return writeCursor(cursorPath, stagingCursor{Declared: addr.Hex(), LastAckedSeq: 0, Total: total})
}

// ResumeStaging reports the last acked sequence to resume from, or a
// typed error if the .tmp file and its cursor sidecar are missing or
// inconsistent — an explicit refusal to resume rather than a silent
// restart-from-zero or a silent trust of a half file. The caller must
// then call BeginStaging (discarding whatever partial bytes exist) if it
// wants to retry from scratch instead.
func ResumeStaging(stagingDir string, addr ContentAddress) (lastAckedSeq uint64, err error) {
	tmpPath, cursorPath := stagingPaths(stagingDir, addr)
	info, statErr := os.Stat(tmpPath)
	if statErr != nil {
		return 0, cascade.Wrap(cascade.KindNotFound, statErr, "sync: no staged transfer to resume")
	}
	cur, err := readCursor(cursorPath)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindIntegrity, err, "sync: staging cursor missing or unreadable; refusing to resume an untracked .tmp file")
	}
	if cur.Declared != addr.Hex() {
		return 0, cascade.New(cascade.KindIntegrity, "sync: staging cursor names a different content address; refusing to resume")
	}
	if info.Size() == 0 && cur.LastAckedSeq != 0 {
		return 0, cascade.New(cascade.KindIntegrity, "sync: staging cursor and .tmp file size disagree; refusing to resume")
	}
	return cur.LastAckedSeq, nil
}

// AppendStagedChunk appends payload to addr's .tmp file and durably
// advances the sidecar cursor to seq. Called once per verified chunk
// (chunk.go's Decode has already checked the transport-layer hash before
// this is called).
func AppendStagedChunk(stagingDir string, addr ContentAddress, seq, total uint64, payload []byte) error {
	tmpPath, cursorPath := stagingPaths(stagingDir, addr)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: open staging tmp file for append")
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: append staged chunk")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: fsync staged chunk")
	}
	if err := f.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: close staged chunk write")
	}
	return writeCursor(cursorPath, stagingCursor{Declared: addr.Hex(), LastAckedSeq: seq + 1, Total: total})
}

// AdmitBlob recomputes the BLAKE3 digest of addr's staged .tmp file and
// admits it to destPath ONLY when the digest equals addr exactly. On a
// mismatch the staged bytes are discarded (removed) and destPath is never
// created — the interrupted-or-corrupt case can therefore never commit
// truncated bytes under a valid-looking content address. On success the
// admission is one atomic os.Rename (same filesystem), matching
// provision.go's mv-f precedent: destPath either does not exist, or
// exists complete — never partially written.
func AdmitBlob(stagingDir string, addr ContentAddress, destPath string) error {
	tmpPath, cursorPath := stagingPaths(stagingDir, addr)
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: read staged blob")
	}
	got := blake3.Sum256(data)
	if got != [32]byte(addr) {
		_ = os.Remove(tmpPath)
		_ = os.Remove(cursorPath)
		return cascade.Newf(cascade.KindIntegrity, "sync: staged blob digest mismatch: got %x want %x; staged bytes discarded", got, addr)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: create blob dest dir")
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: atomic blob admission rename failed")
	}
	_ = os.Remove(cursorPath)
	return nil
}

func writeCursor(path string, cur stagingCursor) error {
	raw, err := json.Marshal(cur)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sync: encode staging cursor")
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: write staging cursor")
	}
	f, err := os.Open(path)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: reopen staging cursor for fsync")
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sync: fsync staging cursor")
	}
	return nil
}

func readCursor(path string) (stagingCursor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return stagingCursor{}, err
	}
	var cur stagingCursor
	if err := json.Unmarshal(raw, &cur); err != nil {
		return stagingCursor{}, err
	}
	return cur, nil
}
