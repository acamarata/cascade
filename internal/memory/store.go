package memory

// Purpose: the MemoryStore contract every memory backend implements, the
//   domain sentinel errors call sites match on, the record-name rule that
//   keeps a name safe to use as a path segment on every supported
//   platform, and the BLAKE3 body-hash helper.
// Inputs: caller-supplied names and bodies.
// Outputs: a typed pkg/cascade error per refusal; a deterministic hex
//   digest per body.
// Constraints: the sentinels here follow pkg/cascade's own rule that
//   domain-specific sentinels live in their owning package and each wraps
//   exactly one frozen Kind; no Kind is invented. Pure functions only,
//   no I/O, no clock.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Domain sentinel errors. Each names one refusal precisely so a caller can
// tell them apart with errors.Is instead of matching message text, and
// each is wrapped by a taxonomy error carrying the frozen Kind that
// classifies it. They are values, not kinds: the 14-kind taxonomy in
// pkg/cascade is closed and none is added here.
var (
	// ErrInvalidKind is returned for a kind outside the ratified four.
	ErrInvalidKind = cascade.New(cascade.KindInvalidInput, "invalid memory kind")
	// ErrInvalidOrigin is returned for an unrecognized provenance origin.
	ErrInvalidOrigin = cascade.New(cascade.KindInvalidInput, "invalid provenance origin")
	// ErrInvalidName is returned for a record name that is not usable as a
	// path segment on every supported platform.
	ErrInvalidName = cascade.New(cascade.KindInvalidInput, "invalid memory record name")
	// ErrInvalidScopeRef is returned for an empty scope reference.
	ErrInvalidScopeRef = cascade.New(cascade.KindInvalidInput, "invalid scope reference")
	// ErrInvalidSupersedes is returned for a supersedes reference that is
	// not a "<kind>/<name>" pair.
	ErrInvalidSupersedes = cascade.New(cascade.KindInvalidInput, "invalid supersedes reference")
	// ErrInvalidConfidence is returned for a confidence outside [0,1].
	ErrInvalidConfidence = cascade.New(cascade.KindInvalidInput, "confidence outside [0,1]")
	// ErrAlreadyExpired is returned when a record is written with a TTL
	// that has already passed, which would store a record no read may
	// ever return.
	ErrAlreadyExpired = cascade.New(cascade.KindInvalidInput, "expires_at is already in the past")
	// ErrNoSuchEntry is returned when a record does not exist, or exists
	// only as a tombstone.
	ErrNoSuchEntry = cascade.New(cascade.KindNotFound, "memory record not found")
	// ErrMalformedEntry is returned when a file exists but cannot be
	// parsed as a memory record. Reads fail closed on it: the entry is
	// refused whole, never returned half-parsed.
	ErrMalformedEntry = cascade.New(cascade.KindIntegrity, "malformed memory record")
	// ErrUnsupportedFormat is returned for a record whose declared format
	// version this build does not know how to read. It is separate from
	// ErrMalformedEntry because it is the forward-compatibility refusal, a
	// file written by a newer build, not a damaged one.
	ErrUnsupportedFormat = cascade.New(cascade.KindUnsupported, "unsupported memory record format version")
	// ErrStoreIO is returned when the underlying file system fails.
	ErrStoreIO = cascade.New(cascade.KindUnavailable, "memory store I/O failure")
)

// MemoryStore is the contract for a memory backend.
//
// Every method returns a pkg/cascade taxonomy error or nil; no raw
// os.PathError ever escapes an implementation. Reads fail closed: an entry
// that cannot be parsed whole is refused with ErrMalformedEntry rather
// than returned partially populated, and one unreadable record never makes
// the rest of the store unreachable.
//
// This ticket's contract names this exact exported symbol; renaming to
// Store to silence revive's package-name-stutter hint would break the
// contract the later tickets in this sprint are written against.
//
//nolint:revive // contract-mandated exported name, see the doc comment above
type MemoryStore interface {
	// Write creates or updates a record. It is idempotent: writing a
	// record that already matches what is stored changes nothing on disk
	// and returns nil.
	Write(ctx context.Context, entry MemoryEntry) error
	// Read returns the record stored under kind and name. It returns
	// ErrNoSuchEntry when there is none or the record is tombstoned, and
	// ErrMalformedEntry when the file cannot be parsed whole.
	Read(ctx context.Context, kind MemoryKind, name string) (MemoryEntry, error)
	// Update rewrites an existing record. It returns ErrNoSuchEntry when
	// the record does not exist, which is the only behaviour that
	// distinguishes it from Write.
	Update(ctx context.Context, entry MemoryEntry) error
	// Delete tombstones a record. It returns ErrNoSuchEntry when there is
	// nothing to delete.
	Delete(ctx context.Context, kind MemoryKind, name string) error
	// List returns the names of every live (non-tombstoned) record of the
	// given kind, in lexical order.
	//
	// It returns names rather than records deliberately. Returning parsed
	// records would make a single damaged file fail the whole listing,
	// which is exactly the "one bad file makes the store unreadable"
	// failure this package refuses to have. Callers read each name and
	// handle each record's own error.
	List(ctx context.Context, kind MemoryKind) ([]string, error)
	// Exists reports whether a live record is stored under kind and name.
	// A tombstoned record does not exist.
	Exists(ctx context.Context, kind MemoryKind, name string) (bool, error)
}

// maxNameLen caps a record name well below the 255-byte filename limit
// every supported file system enforces, leaving room for the ".md" suffix
// and the ".md.tombstone" suffix without either reaching the limit.
const maxNameLen = 128

// windowsReservedStems are device names Windows refuses as a filename,
// with or without an extension. A name valid on Linux but unwritable on
// Windows would make the store's contents platform-dependent, so it is
// refused everywhere rather than on Windows only (Art.5 parity).
var windowsReservedStems = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateName refuses any record name that is not safe and portable as a
// single path segment. The rule is a small allowed charset rather than a
// list of forbidden characters: a deny list has to anticipate every
// separator, device name and encoding trick on every platform, and one
// missed case is a path escape out of the store's base directory.
//
// Allowed: ASCII letters, digits, "." "_" and "-", between 1 and 128
// bytes, not beginning with "." (which would hide the record and, as
// "." or "..", escape the directory), not ending with "." or "-", and not
// a Windows reserved device name.
func ValidateName(name string) error {
	if err := validateNameShape(name); err != nil {
		return err
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidName,
				"name %q contains disallowed byte %q at offset %d", name, name[i], i)
		}
	}
	stem, _, _ := strings.Cut(strings.ToLower(name), ".")
	if windowsReservedStems[stem] {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidName,
			"name %q is a reserved device name on Windows", name)
	}
	return nil
}

// validateNameShape checks the length and edge-character rules. Split from
// ValidateName to keep both inside the 50-line limit.
func validateNameShape(name string) error {
	switch {
	case name == "":
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidName, "name is empty")
	case len(name) > maxNameLen:
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidName,
			"name is %d bytes, over the %d-byte limit", len(name), maxNameLen)
	case strings.HasPrefix(name, "."):
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidName,
			"name %q begins with a dot", name)
	case strings.HasSuffix(name, "."), strings.HasSuffix(name, "-"):
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidName,
			"name %q ends with %q", name, name[len(name)-1:])
	}
	return nil
}

// isNameByte reports whether b is in the allowed record-name charset.
func isNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '.' || b == '_' || b == '-':
		return true
	default:
		return false
	}
}

// HashBody returns the BLAKE3 hex digest of body. It is pure Go with no
// CGO (project law), takes no clock and no randomness, and so returns the
// same digest for the same bytes on every platform and every run, which is
// what makes it usable as the stored provenance hash and as a change
// detector across machines.
func HashBody(body string) string {
	sum := blake3.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
