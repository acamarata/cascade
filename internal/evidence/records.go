package evidence

// Purpose: Evidence is the typed record of one observed byte range backing
// a Claim: where it was observed (Source) and the sha256 of the exact
// bytes recorded.
// Inputs: none (types only, plus crypto/sha256 for ContentHash).
// Outputs: an Evidence value, or a typed invalid-input error.
// Constraints: SourceType is a closed five-value enum with no permissive
// zero value; ContentHash is always the lowercase hex sha256 of the FULL
// byte range a Locator names, computed when the row is written -- never
// of a truncated read.
//
// SPORT: evidence/evidence-record (ADD).

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EvidenceIDPrefix names every minted evidence id.
const EvidenceIDPrefix = "EVD-"

// SourceType is the closed five-value vocabulary an Evidence row's origin
// is classified under (R-21.41). The zero value is deliberately not a
// member.
type SourceType string

const (
	// SourceGit marks evidence read from a git blob at a recorded commit.
	SourceGit SourceType = "git"
	// SourceFile marks evidence captured from a plain file.
	SourceFile SourceType = "file"
	// SourceTestLog marks evidence captured from a test run's log output.
	SourceTestLog SourceType = "test-log"
	// SourceArtifact marks evidence read from the artifact store by ref.
	SourceArtifact SourceType = "artifact"
	// SourceURL marks evidence captured from a fetched URL.
	SourceURL SourceType = "url"
)

// Valid reports whether t is one of the five closed values.
func (t SourceType) Valid() bool {
	switch t {
	case SourceGit, SourceFile, SourceTestLog, SourceArtifact, SourceURL:
		return true
	}
	return false
}

// DecodeSourceType parses a stored value into a SourceType. An unset,
// unknown, or unparseable value is a typed invalid-input error.
func DecodeSourceType(raw string) (SourceType, error) {
	t := SourceType(raw)
	if !t.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"evidence: unknown source type %q: must be one of git, file, test-log, artifact, url", raw)
	}
	return t, nil
}

// Source names where one Evidence row was observed (R-21.41): a source
// type, an optional repository/commit/path (git sources) and the Locator
// naming the exact sub-range within it.
type Source struct {
	Type       SourceType `json:"type"`
	Repository string     `json:"repository"`
	Commit     string     `json:"commit"`
	Path       string     `json:"path"`
	Locator    Locator    `json:"locator"`
}

// Evidence is the R-21.41 evidence record verbatim, plus the two fields
// R-21.94 and R-21.86 add: an immutable DataClass and CapturedRef, the
// artifact-store ref of the captured byte range (capture.go).
type Evidence struct {
	ID          string    `json:"id"`
	ClaimID     string    `json:"claim_id"`
	Source      Source    `json:"source"`
	ContentHash string    `json:"content_hash"`
	ObservedAt  time.Time `json:"observed_at"`
	DataClass   DataClass `json:"data_class"`
	CapturedRef string    `json:"captured_ref"`
}

// NewEvidenceID mints a fresh evidence identifier: EvidenceIDPrefix plus
// the 26-character Crockford-base32 id from pkg/cascade.NewID (R-16.47: no
// google/uuid anywhere in this package).
func NewEvidenceID() (string, error) {
	id, err := cascade.NewID()
	if err != nil {
		return "", err
	}
	return EvidenceIDPrefix + id.String(), nil
}

// ContentHash returns the lowercase hex sha256 of b -- the FULL byte range
// a Locator names, never a truncated read.
func ContentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// unixToTime converts a stored unix-seconds column into a UTC time.Time.
// Shared by every store file in this package that scans a timestamp
// column.
func unixToTime(u int64) time.Time {
	return time.Unix(u, 0).UTC()
}

// Validate rejects an unknown SourceType, a missing ClaimID, an unknown
// DataClass, or an empty ContentHash -- every rejection is a typed
// invalid-input error.
func (e Evidence) Validate() error {
	if e.ClaimID == "" {
		return cascade.New(cascade.KindInvalidInput, "evidence: evidence claim_id is required")
	}
	if !e.Source.Type.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "evidence: unknown source type %q", string(e.Source.Type))
	}
	if !e.DataClass.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "evidence: unknown data_class %q", string(e.DataClass))
	}
	if e.ContentHash == "" {
		return cascade.New(cascade.KindInvalidInput, "evidence: evidence content_hash is required")
	}
	if !e.Source.Locator.Kind.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "evidence: unknown locator kind %q", string(e.Source.Locator.Kind))
	}
	return nil
}
