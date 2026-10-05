// Purpose: the quarantine store - the local, append-only ledger of what the
// detector flagged, why, and what happened to it afterwards.
// Inputs: a DetectionHit and a source reference. NEVER the flagged bytes: Put
// has no parameter that could carry a value, so a leak is a compile error.
// Outputs: QuarantineEntry records in <dir>/quarantine.jsonl (0600), an id
// each, and the fingerprint key <dir>/quarantine.key (0600).
// Constraints: APPEND-ONLY and REVERSIBLE. Delete appends a release record, so
// "what was quarantined, and what became of it" is always answerable; every
// entry has two recorded exits (promoted into the vault, or released as a
// false positive). No network, an injected clock, no map iteration in output.
//
// SPORT: QUARANTINE_STORE: ADD (internal/secrets.QuarantineStore,
//
//	QuarantineEntry).

package secrets

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Clock is the injected time source (02-TARGET-STRUCTURE §v1.1: no bare
// time.Now in domain logic). It is structurally identical to
// internal/runtime.Clock, which cmd/cascade passes in; declaring it here
// keeps the store's constructor signature free of internal/runtime types.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// quarantineLogName and quarantineKeyName are the two files the store
// owns inside its directory; quarantineKeyBytes is the key file's only
// accepted length.
const (
	quarantineLogName  = "quarantine.jsonl"
	quarantineKeyName  = "quarantine.key"
	quarantineKeyBytes = 32
	fingerprintBytes   = 16
	// quarantineKeyRecovery ends every key refusal: the recovery step, never the key.
	quarantineKeyRecovery = "it was left untouched. To recover, confirm no quarantined fingerprints are needed, " +
		"move the file aside, and open the store again to create a new key"
)

// Release reasons recorded on a Delete, so the ledger says what happened
// rather than only that something did.
const (
	// ReleasePromoted means the entry became a vault secret.
	ReleasePromoted = "promoted"
	// ReleaseFalsePositive means an operator judged the detection wrong.
	ReleaseFalsePositive = "false-positive"
)

// QuarantineEntry is one recorded detection. Every field is metadata: a
// class, a location, a score, a name suggestion, and a keyed fingerprint.
// There is no field that holds, encodes or encrypts the flagged bytes.
type QuarantineEntry struct {
	// ID is the deterministic identifier, hex. Stable for the same
	// (class, offset, content) under the same store key.
	ID string `json:"id"`
	// Class is the detected credential class.
	Class Class `json:"class"`
	// Pattern names the registry pattern that fired.
	Pattern string `json:"pattern"`
	// Offset and Length locate the span inside the source.
	Offset int `json:"offset"`
	Length int `json:"length"`
	// Confidence is the detector's score for the hit.
	Confidence Confidence `json:"confidence"`
	// SuggestedName is the UPPER_SNAKE vault name to promote under.
	SuggestedName string `json:"suggested_name"`
	// SourceRef is the caller's reference to WHERE the content came
	// from - a memory record id, a document path. Never the content.
	SourceRef string `json:"source_ref"`
	// Fingerprint is a KEYED BLAKE3 digest of the flagged bytes,
	// truncated. Keyed, with a per-store key that never leaves the
	// machine, so a quarantine log pasted into a bug report cannot be
	// dictionary-attacked back to a short secret the way a bare hash
	// could. It exists only to recognise the same finding twice.
	Fingerprint string `json:"fingerprint"`
	// DetectedAt is when the detection was recorded.
	DetectedAt time.Time `json:"detected_at"`
}

// quarantineRecord is one line of the log: an entry plus what the line
// does. "put" records a detection, "release" retires one with a reason.
type quarantineRecord struct {
	Op     string          `json:"op"`
	Reason string          `json:"reason,omitempty"`
	At     time.Time       `json:"at"`
	Entry  QuarantineEntry `json:"entry"`
}

// QuarantineStore is the append-only quarantine ledger. Build one with
// NewQuarantineStore; the zero value is not usable.
type QuarantineStore struct {
	dir   string
	clock Clock
	mu    sync.Mutex
	key   []byte
	// keyCreated: this open published the key (CreateFileAtomic created=true).
	// Test-observable only: no behaviour may depend on it.
	keyCreated bool
}

// NewQuarantineStore opens (or creates) the store under dir. The
// directory is created 0700 and every file inside it 0600: the ledger is
// daemon-uid-only, because even metadata about where an operator's
// credentials appear is worth keeping to one account.
func NewQuarantineStore(dir string, clock Clock) (*QuarantineStore, error) {
	if dir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: quarantine store needs a directory")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: quarantine store needs a clock")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "secrets: could not create the quarantine directory %s", dir)
	}
	store := &QuarantineStore{dir: dir, clock: clock}
	key, created, err := store.loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	store.key, store.keyCreated = key, created
	return store, nil
}

// logPath is the ledger file's path.
func (q *QuarantineStore) logPath() string { return filepath.Join(q.dir, quarantineLogName) }

// loadOrCreateKey reads the fingerprint key, creating it on first use from
// crypto/rand (cryptorand alias: the CSPRNG, per Art.7.3's forbidigo rule)
// via runtime.CreateFileAtomic (0600, exclusive): one opener creates it, the
// rest re-read it through the same checked reader. Never regenerated.
func (q *QuarantineStore) loadOrCreateKey() (key []byte, created bool, err error) {
	path := filepath.Join(q.dir, quarantineKeyName)
	key, absent, err := readQuarantineKey(path)
	if !absent {
		return key, false, err
	}
	fresh := make([]byte, quarantineKeyBytes)
	if _, rerr := cryptorand.Read(fresh); rerr != nil {
		return nil, false, cascade.Wrap(cascade.KindInternal, rerr, "secrets: could not generate a quarantine key")
	}
	created, err = runtime.CreateFileAtomic(path, fresh, 0o600)
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not create the quarantine key")
	}
	if created {
		return fresh, true, nil
	}
	key, absent, err = readQuarantineKey(path)
	if absent {
		return nil, false, cascade.Newf(cascade.KindUnavailable, "secrets: the quarantine key %s vanished after another opener created it", path)
	}
	if err != nil {
		kind, _ := cascade.KindOf(err)
		return nil, false, cascade.Wrap(kind, err, "secrets: the quarantine key another opener created is unusable")
	}
	return key, false, nil
}

// readQuarantineKey reads the key at path; absent is true only when nothing
// is there. A non-regular file (symlink, dangling or not, FIFO, directory,
// device, socket) is refused before any open with KindIntegrity, not a
// retryable kind, since an operator must move it aside: a FIFO cannot block
// and a link cannot point the key at a wider-mode file elsewhere.
func readQuarantineKey(path string) (key []byte, absent bool, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, cascade.Wrapf(cascade.KindUnavailable, err, "secrets: could not stat the quarantine key %s", path)
	}
	if !info.Mode().IsRegular() {
		name, ok := keyPathTypes[info.Mode().Type()]
		if !ok {
			name = "special file"
		}
		return nil, false, cascade.Newf(cascade.KindIntegrity, "secrets: the quarantine key path %s is a %s, not a regular file; %s",
			path, name, quarantineKeyRecovery)
	}
	key, err = readCheckedKey(path, info)
	return key, false, err
}

// readCheckedKey opens path non-blocking and refuses unless the opened file is
// the regular file checked describes (unix: Lstat dev/inode, so any swap is refused;
// Windows reloads the path id at compare time: a re-pointed link is refused, a
// swapped-in regular file is not). Length != 32 is KindIntegrity, never bytes.
func readCheckedKey(path string, checked os.FileInfo) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // fixed name under the caller's data dir
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "secrets: could not open the quarantine key %s", path)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(checked, opened) {
		return nil, cascade.Wrapf(cascade.KindIntegrity, err, "secrets: the opened quarantine key %s is not the regular file "+
			"checked a moment before (swapped, or unconfirmable); %s", path, quarantineKeyRecovery)
	}
	key, err := readBoundedKey(f)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "secrets: could not read the quarantine key %s", path)
	}
	if len(key) == quarantineKeyBytes {
		return key, nil
	}
	return nil, cascade.Newf(cascade.KindIntegrity, "secrets: the quarantine key %s holds %d bytes, want %d; %s",
		path, max(opened.Size(), int64(len(key))), quarantineKeyBytes, quarantineKeyRecovery)
}

// readBoundedKey reads at most one byte past a key: enough to refuse a longer file unread.
func readBoundedKey(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, quarantineKeyBytes+1))
}

// keyPathTypes names the non-regular types a key refusal reports by name.
var keyPathTypes = map[os.FileMode]string{os.ModeSymlink: "symbolic link", os.ModeDir: "directory", os.ModeNamedPipe: "named pipe"}

// fingerprint returns the keyed, truncated digest of value: BLAKE3 over
// the store key followed by the value. A prefix construction is sound
// here because BLAKE3 is not length-extendable, and it keeps the function
// total - there is no error path to test and no unreachable branch to
// leave uncovered.
func (q *QuarantineStore) fingerprint(value []byte) string {
	h := blake3.New()
	_, _ = h.Write(q.key) // hash.Hash.Write never returns an error
	_, _ = h.Write(value)
	return hex.EncodeToString(h.Sum(nil)[:fingerprintBytes])
}

// Put records hit against sourceRef and returns the stored entry.
//
// value is the flagged bytes, used ONLY for the keyed fingerprint and never
// written, logged or retained; nil yields an empty fingerprint, nothing else.
func (q *QuarantineStore) Put(hit DetectionHit, sourceRef string, value []byte) (QuarantineEntry, error) {
	if hit.SuggestedName == "" {
		return QuarantineEntry{}, cascade.New(cascade.KindInvalidInput,
			"secrets: a quarantine entry needs a suggested name to be promotable")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := QuarantineEntry{
		Class: hit.Class, Pattern: hit.Pattern, Offset: hit.Offset, Length: hit.Len,
		Confidence: hit.Confidence, SuggestedName: hit.SuggestedName, SourceRef: sourceRef,
		DetectedAt: q.clock.Now().UTC(),
	}
	if len(value) > 0 {
		entry.Fingerprint = q.fingerprint(value)
	}
	entry.ID = q.entryID(entry)
	if err := q.appendLocked(quarantineRecord{Op: "put", At: entry.DetectedAt, Entry: entry}); err != nil {
		return QuarantineEntry{}, err
	}
	return entry, nil
}

// entryID is the deterministic id: a keyed digest over the class, the
// byte offset and the content fingerprint, exactly the triple the ticket
// specifies. Keyed for the same reason the fingerprint is.
func (q *QuarantineStore) entryID(entry QuarantineEntry) string {
	material := string(entry.Class) + "\x00" + strconv.Itoa(entry.Offset) + "\x00" +
		entry.Fingerprint + "\x00" + entry.SourceRef
	return q.fingerprint([]byte(material))
}

// appendLocked writes one record and flushes it. O_APPEND so a second
// writer cannot overwrite the first's line, and an fsync so a record the
// caller was told about survives a crash.
func (q *QuarantineStore) appendLocked(rec quarantineRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "secrets: could not encode the quarantine record")
	}
	f, err := os.OpenFile(q.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // fixed name under the caller's data dir
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not open the quarantine log")
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not append to the quarantine log")
	}
	return f.Sync()
}
