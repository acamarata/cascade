package memory

// Purpose: the on-disk record codec: the frontmatter block that carries
//   every MemoryEntry field except the body, and the body that follows it.
//   This file IS the format contract: the checked-in fixtures under
//   testdata/v1-goldens are what it must produce and must accept, and a
//   change here that those fixtures do not also make is a change that
//   corrupts or drops records already on disk.
// Inputs: a MemoryEntry to encode; raw file bytes to decode.
// Outputs: canonical bytes; a fully-populated MemoryEntry, or a typed
//   refusal.
// Constraints: deterministic (fixed key order, no map iteration reaching
//   the output, shortest-exact float formatting); fails closed on every
//   malformed, truncated, wrongly-typed or unknown-version input, and
//   never returns a partially populated record alongside an error.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// formatVersion is the on-disk record format this build writes. A file
// declaring any other version is refused with ErrUnsupportedFormat rather
// than read on a best-effort basis: guessing at a layout written by a
// build that knew more than this one is how records get silently dropped.
const formatVersion = 1

// fence is the frontmatter delimiter line, the conventional markdown
// frontmatter fence.
const fence = "---"

// Frontmatter keys. Written in exactly this order, every key on every
// record, so two records with the same content produce byte-identical
// files on any machine. Reading accepts them in any order but requires
// all of them: a missing key means the writer and this reader disagree
// about the format, which is a refusal, not a default.
const (
	keyFormat      = "format"
	keyName        = "name"
	keyKind        = "kind"
	keyDescription = "description"
	keyScopeRef    = "scope_ref"
	keyCommitSHA   = "commit_sha"
	keySupersedes  = "supersedes"
	keyExpiresAt   = "expires_at"
	keyConfidence  = "confidence"
	keyOrigin      = "origin"
	keySessionID   = "session_id"
	keyCreatedAt   = "created_at"
	keyUpdatedAt   = "updated_at"
	keyContentHash = "content_hash"
)

// orderedKeys is the fixed write order and the exact required key set.
var orderedKeys = []string{
	keyFormat, keyName, keyKind, keyDescription, keyScopeRef,
	keyCommitSHA, keySupersedes, keyExpiresAt, keyConfidence,
	keyOrigin, keySessionID, keyCreatedAt, keyUpdatedAt, keyContentHash,
}

// timeLayout is the timestamp encoding: RFC3339 with nanoseconds, always
// in UTC. Parsing recovers the exact instant it was written from.
const timeLayout = time.RFC3339Nano

// encodeEntry renders e as the canonical on-disk bytes. e must already be
// canonical (see MemoryEntry.canonical); encoding does not silently fix a
// non-UTC timestamp, because a codec that repairs its input hides the
// caller's bug until the day the file is read on another machine.
func encodeEntry(e MemoryEntry) []byte {
	var b bytes.Buffer
	b.WriteString(fence + "\n")
	writeField(&b, keyFormat, strconv.Itoa(formatVersion))
	writeField(&b, keyName, quote(e.Name))
	writeField(&b, keyKind, quote(string(e.Kind)))
	writeField(&b, keyDescription, quote(e.Description))
	writeField(&b, keyScopeRef, quote(e.ScopeRef))
	writeField(&b, keyCommitSHA, quote(e.CommitSHA))
	writeField(&b, keySupersedes, quote(e.Supersedes))
	writeField(&b, keyExpiresAt, quote(formatOptionalTime(e.ExpiresAt)))
	writeField(&b, keyConfidence, strconv.FormatFloat(e.Confidence, 'g', -1, 64))
	writeField(&b, keyOrigin, quote(string(e.Provenance.Origin)))
	writeField(&b, keySessionID, quote(e.Provenance.SessionID))
	writeField(&b, keyCreatedAt, quote(e.Provenance.CreatedAt.Format(timeLayout)))
	writeField(&b, keyUpdatedAt, quote(e.Provenance.UpdatedAt.Format(timeLayout)))
	writeField(&b, keyContentHash, quote(e.Provenance.ContentHash))
	b.WriteString(fence + "\n")
	b.WriteString(e.Body)
	return b.Bytes()
}

// writeField appends one "key: value" line.
func writeField(b *bytes.Buffer, key, value string) {
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\n")
}

// quote renders s as a JSON string literal, which is also a valid
// double-quoted YAML scalar. Every string value is quoted this way, with
// no exceptions and no bare scalars: hand-rolled YAML quoting rules are
// where a value containing a colon, a newline, a leading "#", or a quote
// character stops surviving the round trip, and this format's whole job is
// surviving the round trip.
func quote(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string fails only on invalid UTF-8, which it
		// encodes as the replacement character rather than erroring, so
		// this branch is unreachable. Falling back to strconv.Quote keeps
		// the function total rather than panicking.
		return strconv.Quote(s)
	}
	return string(out)
}

// formatOptionalTime renders a TTL, or the empty string for no TTL.
func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(timeLayout)
}

// decodeEntry parses raw file bytes into a record. Every failure path
// returns the zero MemoryEntry alongside the error, so a caller that
// ignores the error still cannot act on half a record.
func decodeEntry(data []byte) (MemoryEntry, error) {
	header, body, err := splitFrontmatter(data)
	if err != nil {
		return MemoryEntry{}, err
	}
	// The version is read before anything else about the key set is
	// judged. A record written by a future build will carry keys this one
	// has never heard of, and reporting that as "unknown key" would tell
	// the operator the file is damaged when it is merely newer.
	if err := checkFormatVersion(header); err != nil {
		return MemoryEntry{}, err
	}
	fields, err := parseFields(header)
	if err != nil {
		return MemoryEntry{}, err
	}
	return buildEntry(fields, body)
}

// splitFrontmatter separates the frontmatter lines from the body. It
// requires an opening fence on the first line and a closing fence
// somewhere after it; a file with neither, or with an opening fence and no
// closing one (the shape a truncated write leaves), is refused.
func splitFrontmatter(data []byte) (header []string, body string, err error) {
	text := string(data)
	rest, ok := cutFenceLine(text)
	if !ok {
		return nil, "", cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"file does not begin with a %q frontmatter fence", fence)
	}
	for rest != "" {
		line, after, hasNewline := strings.Cut(rest, "\n")
		if !hasNewline {
			break
		}
		if strings.TrimRight(line, "\r") == fence {
			return header, after, nil
		}
		header = append(header, strings.TrimRight(line, "\r"))
		rest = after
	}
	return nil, "", cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
		"frontmatter is not closed by a %q fence (truncated file)", fence)
}

// cutFenceLine removes a leading fence line, tolerating CRLF endings that
// a checkout on a platform with line-ending translation may introduce.
func cutFenceLine(text string) (rest string, ok bool) {
	for _, prefix := range []string{fence + "\n", fence + "\r\n"} {
		if strings.HasPrefix(text, prefix) {
			return text[len(prefix):], true
		}
	}
	return "", false
}

// parseFields turns frontmatter lines into a key/value map, refusing an
// unparseable line, an unknown key, a duplicate key, or a missing key.
// Every one of those means this reader and the writer disagree about the
// format, and a reader that shrugs at that is how a field silently stops
// being persisted.
func parseFields(lines []string) (map[string]string, error) {
	fields := make(map[string]string, len(orderedKeys))
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
				"frontmatter line %q is not a key/value pair", line)
		}
		key = strings.TrimSpace(key)
		if !knownKey(key) {
			return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
				"frontmatter has unknown key %q", key)
		}
		if _, dup := fields[key]; dup {
			return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
				"frontmatter repeats key %q", key)
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields, checkComplete(fields)
}

// knownKey reports whether key is part of this format version.
func knownKey(key string) bool {
	for _, k := range orderedKeys {
		if k == key {
			return true
		}
	}
	return false
}

// checkComplete refuses a frontmatter block missing any required key.
func checkComplete(fields map[string]string) error {
	for _, k := range orderedKeys {
		if _, ok := fields[k]; !ok {
			return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
				"frontmatter is missing required key %q", k)
		}
	}
	return nil
}
