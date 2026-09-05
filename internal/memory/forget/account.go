package forget

// Purpose: the forget account — the durable record of one retirement,
//   written BEFORE anything is removed and updated after each step. It is
//   what turns an interrupted forget from a half-present record into a
//   recorded, resumable one, and it is the file that says what was
//   forgotten, when and why.
// Inputs: a canonical address, a caller's reason, and the injected clock's
//   instant; raw file bytes on the way back in.
// Outputs: canonical bytes under
//   {base}/forgotten/{kind}/{name}.forget.json, or a typed pkg/cascade
//   refusal.
// Constraints: fails closed on a malformed or unknown-version file rather
//   than overwriting it, for the reason the consolidation record gives:
//   this file is the only surviving account of something that was removed,
//   and guessing at a layout a newer build wrote is how that account gets
//   silently truncated. It holds the ADDRESS and the reason, never the
//   record's body: an account of a forgetting that kept the text would
//   defeat the request it records.
// SPORT: internal/memory/forget (ADD, P1-E07-W2-S14-T4).

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// accountSchemaVersion is the account format this build writes.
const accountSchemaVersion = 1

// accountsDir and accountSuffix are where and how an account is filed.
// They sit outside the record tree, beside the consolidation records, so
// nothing walking a kind's directory mistakes an account for a memory.
const (
	accountsDir   = "forgotten"
	accountSuffix = ".forget.json"
)

// Account sentinel errors.
var (
	// ErrMalformedAccount is returned when an account file exists but
	// cannot be parsed whole.
	ErrMalformedAccount = cascade.New(cascade.KindIntegrity,
		"malformed memory forget account")
	// ErrUnsupportedAccountFormat is the forward-compatibility refusal for
	// an account written by a newer build.
	ErrUnsupportedAccountFormat = cascade.New(cascade.KindUnsupported,
		"unsupported memory forget account format version")
)

// account is one retirement as stored.
//
// The four fields the tombstone semantics are specified against live here
// verbatim: the entity id, the instant the record was removed, the reason,
// and the schema version. They are in this file rather than inside the
// store's tombstone marker because the marker is a zero-byte sentinel the
// projection scans for, and widening it into a parsed document would make
// every deletion in the store depend on a codec. The marker keeps the
// deletion durable; this file explains it.
type account struct {
	// SchemaVersion is the account format version.
	SchemaVersion int `json:"schema_version"`
	// EntityID is the canonical "<kind>/<name>" address being retired.
	EntityID string `json:"entity_id"`
	// Reason is the caller's stated reason, verbatim and possibly empty.
	Reason string `json:"reason,omitempty"`
	// RequestedAt is when the retirement was asked for, from the injected
	// clock. It is written before any step runs, so an interrupted forget
	// is dated even if nothing else about it completed.
	RequestedAt time.Time `json:"requested_at"`
	// IndexScrubbed records that the derived index no longer holds this
	// address.
	IndexScrubbed bool `json:"index_scrubbed"`
	// Tombstoned records that the store has retired the record.
	Tombstoned bool `json:"tombstoned"`
	// DeletedAt is when the record was removed, or nil while it has not
	// been. It is the contract's deleted_at.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	// EventEmitted records that the backup lane was told.
	EventEmitted bool `json:"event_emitted"`
	// Completed is true only when every step above finished. It is what a
	// second call reads to decide there is nothing left to do, so it is
	// set last and never optimistically.
	Completed bool `json:"completed"`
}

// accountPath returns the on-disk path of one address's account.
func accountPath(base string, kind memory.MemoryKind, name string) string {
	return filepath.Join(base, accountsDir, string(kind), name+accountSuffix)
}

// newAccount starts an account for a retirement asked for at now.
func newAccount(id, reason string, now time.Time) account {
	return account{
		SchemaVersion: accountSchemaVersion,
		EntityID:      id,
		Reason:        reason,
		RequestedAt:   now.UTC(),
	}
}

// encodeAccount renders an account as canonical, deterministic bytes: a
// fixed field order from the struct, two-space indentation, and a trailing
// newline so the file is a well-formed text file a user can read.
func encodeAccount(a account) ([]byte, error) {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, err,
			"encoding the forget account for %s", a.EntityID)
	}
	return append(data, '\n'), nil
}

// decodeAccount parses an account, failing closed on anything it cannot
// read whole or whose version it does not know.
func decodeAccount(data []byte) (account, error) {
	var a account
	if err := json.Unmarshal(data, &a); err != nil {
		return account{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedAccount,
			"parsing the forget account: %v", err)
	}
	if a.SchemaVersion != accountSchemaVersion {
		return account{}, cascade.Wrapf(cascade.KindUnsupported, ErrUnsupportedAccountFormat,
			"forget account declares schema version %d, this build writes %d",
			a.SchemaVersion, accountSchemaVersion)
	}
	if a.EntityID == "" {
		return account{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedAccount,
			"forget account names no entity")
	}
	return a, nil
}

// loadAccount reads the account at path. A missing file is "no account",
// not an error: the first forget of an address legitimately has none. A
// file that exists and cannot be read whole is refused, never treated as
// missing, because starting a fresh account over an unreadable one would
// destroy the only surviving explanation of an earlier retirement.
func loadAccount(path string) (account, bool, error) {
	data, err := readFile(path)
	if err != nil {
		if isNotExist(err) {
			return account{}, false, nil
		}
		return account{}, false, cascade.Wrapf(cascade.KindUnavailable, memory.ErrStoreIO,
			"reading the forget account at %s: %v", filepath.Base(path), err)
	}
	acct, derr := decodeAccount(data)
	if derr != nil {
		return account{}, false, derr
	}
	return acct, true, nil
}

// saveAccount publishes an account atomically.
func saveAccount(path string, a account) error {
	data, err := encodeAccount(a)
	if err != nil {
		return err
	}
	if werr := writeAtomic(path, data); werr != nil {
		return cascade.Wrapf(cascade.KindUnavailable, memory.ErrStoreIO,
			"writing the forget account for %s: %v", a.EntityID, werr)
	}
	return nil
}
