package promotion

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// recordSchemaVersion is the only schema_version Validate accepts.
const recordSchemaVersion = 1

// MaxRecordBytes caps a serialized record (the promote dispatch input
// record_json is limited to the same size).
const MaxRecordBytes = 64 << 10

var (
	rcTagRe    = regexp.MustCompile(`^v2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$`)
	finalTagRe = regexp.MustCompile(`^v2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	commitRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// GateReport names the signed release-gate report a record was built
// against (P1-EXIT-13 contract) and the sha256 of its bytes.
type GateReport struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Record is the promotion record (contract:promotion-record, schema 1): the
// digests of every file in a signed rc set, keyed so the byte-identity
// verifier can compare staging and public copies against it.
type Record struct {
	SchemaVersion   int               `json:"schema_version"`
	RCTag           string            `json:"rc_tag"`
	FinalTag        string            `json:"final_tag"`
	CommitSHA       string            `json:"commit_sha"`
	ChecksumsSHA256 string            `json:"checksums_sha256"`
	SignatureSHA256 string            `json:"signature_sha256"`
	Archives        map[string]string `json:"archives"`
	Assets          map[string]string `json:"assets"`
	Images          map[string]string `json:"images"`
	GateReport      GateReport        `json:"gate_report"`
	PromotedAt      string            `json:"promoted_at"`
}

// invalid builds a KindInvalidInput refusal naming the field.
func invalid(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "promotion: "+format, args...)
}

// ParseRecord decodes a serialized record strictly (size cap, no unknown
// fields, no trailing data) and validates it.
func ParseRecord(data []byte) (Record, error) {
	if len(data) > MaxRecordBytes {
		return Record{}, invalid("record is %d bytes, over the %d byte limit", len(data), MaxRecordBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Record
	if err := dec.Decode(&r); err != nil {
		return Record{}, invalid("record is not valid JSON for schema %d: %v", recordSchemaVersion, err)
	}
	if dec.More() {
		return Record{}, invalid("record has trailing data")
	}
	if err := Validate(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// Validate checks every field of r against schema 1. It never reads files.
func Validate(r Record) error {
	if r.SchemaVersion != recordSchemaVersion {
		return invalid("schema_version is %d, want %d", r.SchemaVersion, recordSchemaVersion)
	}
	final, err := finalFromRC(r.RCTag)
	if err != nil {
		return err
	}
	if r.FinalTag != final {
		return invalid("final_tag %q is not rc_tag without its -rc suffix", r.FinalTag)
	}
	if !commitRe.MatchString(r.CommitSHA) {
		return invalid("commit_sha is not 40 lowercase hex characters")
	}
	if !sha256Re.MatchString(r.ChecksumsSHA256) || !sha256Re.MatchString(r.SignatureSHA256) {
		return invalid("checksums_sha256 or signature_sha256 is not a sha256 hex digest")
	}
	if err := validateMaps(r); err != nil {
		return err
	}
	if r.GateReport.Path == "" || !sha256Re.MatchString(r.GateReport.SHA256) {
		return invalid("gate_report needs a path and a sha256 hex digest")
	}
	at, err := time.Parse(time.RFC3339, r.PromotedAt)
	if err != nil || at.Location() != time.UTC || !strings.HasSuffix(r.PromotedAt, "Z") {
		return invalid("promoted_at %q is not an RFC 3339 UTC time", r.PromotedAt)
	}
	return nil
}

// validateMaps checks archives, assets and images.
func validateMaps(r Record) error {
	keys := make([]string, 0, len(r.Archives))
	for k, d := range r.Archives {
		keys = append(keys, k)
		if !sha256Re.MatchString(d) {
			return invalid("archives[%s] is not a sha256 hex digest", k)
		}
	}
	slices.Sort(keys)
	if !slices.Equal(keys, platforms) {
		return invalid("archives keys are %v, want exactly %v", keys, platforms)
	}
	if _, ok := r.Assets[imagesName]; !ok {
		return invalid("assets lacks %s", imagesName)
	}
	for name, d := range r.Assets {
		if err := checkLineName(name); err != nil {
			return err
		}
		if !sha256Re.MatchString(d) {
			return invalid("assets[%s] is not a sha256 hex digest", name)
		}
	}
	return checkImages(r.Images)
}

// finalFromRC returns rcTag without its -rc.N suffix, refusing a bad tag.
func finalFromRC(rcTag string) (string, error) {
	if !rcTagRe.MatchString(rcTag) {
		return "", invalid("rc_tag %q is not v2.Y.Z-rc.N", rcTag)
	}
	return rcTag[:strings.Index(rcTag, "-rc.")], nil
}
