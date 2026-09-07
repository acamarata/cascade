package tailer

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: versioned JSONL line decoder for the cc and codex transcript
//   formats, dispatching on each harness's own discriminant version
//   signal per docs/adrs/ADR-E09T6-harness-transcript-stability.md.
// Inputs: one transcript line's raw bytes and its 1-based line number.
// Outputs: a rawEvent (never returned to callers — redact.go reduces it
//   to the allowlisted Record) or a *ParseError.
// Constraints: fail-closed per the ADR's recommended strategy — an
//   unparseable line or an unresolved version key is always a
//   *ParseError, never a skip-and-continue-silently or a best-effort
//   partial decode. ParseError carries only the line number, byte
//   length, and a typed cause; it never carries the offending line's
//   bytes, because a malformed line is exactly where a leaked credential
//   would sit (R-21.152).
// SPORT: fleet/tailer (ADD, per T-2 sport_updates).

// Harness identifies which coding harness wrote the transcript a Tailer
// is following.
type Harness int

const (
	// HarnessUnknown is the zero value; no parser recognizes it.
	HarnessUnknown Harness = iota
	// HarnessCC is the cc coding harness's line-oriented JSONL transcript.
	HarnessCC
	// HarnessCodex is the codex coding harness's rollout JSONL transcript.
	HarnessCodex
)

// String returns the harness's stable lowercase name.
func (h Harness) String() string {
	switch h {
	case HarnessCC:
		return "cc"
	case HarnessCodex:
		return "codex"
	case HarnessUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// ParseError reports that one transcript line could not be decoded: it is
// either malformed JSON or carries a version key this parser does not
// recognize. Per R-21.152 it carries only the line number, the line's
// byte length, and a typed Cause — never the line's content.
type ParseError struct {
	// Line is the 1-based line number within the current open file (reset
	// to 0 on rotation, since rotation starts a new file).
	Line int
	// ByteLen is the offending line's length in bytes, excluding its
	// trailing newline.
	ByteLen int
	// Cause is the typed underlying reason, always a *cascade.Error.
	Cause error
}

// Error implements the error interface without ever echoing line content.
func (e *ParseError) Error() string {
	return fmt.Sprintf("tailer: parse error at line %d (%d bytes): %v", e.Line, e.ByteLen, e.Cause)
}

// Unwrap exposes Cause so errors.Is/errors.As traverse into the taxonomy
// Kind it wraps.
func (e *ParseError) Unwrap() error { return e.Cause }

// rawEvent is the internal, pre-redaction decode of one transcript line.
// It is never returned to a Tailer caller; redact.go reduces it to the
// allowlisted Record. Fields beyond kind/versionKey are populated only
// when a harness line is observed to carry a tool-call's structured data
// (name/duration/exit code/paths/hash) — see redact.go's doc comment for
// why no currently committed fixture exercises that path yet.
type rawEvent struct {
	kind       string
	versionKey string

	tool       string
	durationMS int64
	hasExit    bool
	exitCode   int
	paths      []string
	resultHash string
}

// parser holds per-harness dispatch state that must survive across lines
// within one open file: codex's version signal appears once, on the
// first session_meta line, not on every line.
type parser struct {
	harness Harness

	codexSawMeta bool
	codexVersion string
}

func newParser(h Harness) *parser { return &parser{harness: h} }

// reset clears cross-line state. Tailer calls this whenever it reopens
// the file (rotation or truncation), since a new file starts a new
// session and codex's session_meta requirement starts over.
func (p *parser) reset() {
	p.codexSawMeta = false
	p.codexVersion = ""
}

func (p *parser) parseLine(line []byte, lineNo int) (rawEvent, error) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(line, &generic); err != nil {
		return rawEvent{}, &ParseError{
			Line: lineNo, ByteLen: len(line),
			Cause: cascade.Wrap(cascade.KindInvalidInput, err, "malformed transcript line"),
		}
	}
	switch p.harness {
	case HarnessCC:
		return p.parseCC(generic, lineNo, len(line))
	case HarnessCodex:
		return p.parseCodex(generic, lineNo, len(line))
	case HarnessUnknown:
		return rawEvent{}, &ParseError{
			Line: lineNo, ByteLen: len(line),
			Cause: cascade.New(cascade.KindUnsupported, "unrecognized harness"),
		}
	default:
		return rawEvent{}, &ParseError{
			Line: lineNo, ByteLen: len(line),
			Cause: cascade.New(cascade.KindUnsupported, "unrecognized harness"),
		}
	}
}

// parseCC decodes one cc transcript line. "type" is required on every
// line; "version" is present on message-bearing lines but absent on
// queue-operation/last-prompt lines (ADR field-stability table), so its
// absence is not an error. A present version must parse as the observed
// dotted-numeric shape (e.g. "2.1.140") or the line is UNRESOLVED per the
// ADR's fail-closed rule.
func (p *parser) parseCC(generic map[string]json.RawMessage, lineNo, byteLen int) (rawEvent, error) {
	var kind string
	if raw, ok := generic["type"]; ok {
		_ = json.Unmarshal(raw, &kind)
	}
	if kind == "" {
		return rawEvent{}, &ParseError{
			Line: lineNo, ByteLen: byteLen,
			Cause: cascade.New(cascade.KindInvalidInput, "cc line missing required type field"),
		}
	}
	var version string
	if raw, ok := generic["version"]; ok {
		_ = json.Unmarshal(raw, &version)
		if !isDottedNumericVersion(version) {
			return rawEvent{}, &ParseError{
				Line: lineNo, ByteLen: byteLen,
				Cause: cascade.Newf(cascade.KindUnsupported, "unresolved cc transcript version %q", version),
			}
		}
	}
	return rawEvent{kind: kind, versionKey: version}, nil
}

// parseCodex decodes one codex transcript line. cli_version is stamped
// once, inside the first session_meta line, not per line (ADR). A
// transcript that starts mid-session (no session_meta seen yet) is
// refused per the ADR's buffering rule, rather than guessed at.
func (p *parser) parseCodex(generic map[string]json.RawMessage, lineNo, byteLen int) (rawEvent, error) {
	var kind string
	if raw, ok := generic["type"]; ok {
		_ = json.Unmarshal(raw, &kind)
	}
	if kind == "" {
		return rawEvent{}, &ParseError{
			Line: lineNo, ByteLen: byteLen,
			Cause: cascade.New(cascade.KindInvalidInput, "codex line missing required type field"),
		}
	}
	if kind == "session_meta" {
		version, err := extractCLIVersion(generic["payload"])
		if err != nil {
			return rawEvent{}, &ParseError{Line: lineNo, ByteLen: byteLen, Cause: err}
		}
		p.codexSawMeta = true
		p.codexVersion = version
		return rawEvent{kind: kind, versionKey: version}, nil
	}
	if !p.codexSawMeta {
		return rawEvent{}, &ParseError{
			Line: lineNo, ByteLen: byteLen,
			Cause: cascade.Newf(cascade.KindUnsupported, "codex transcript missing session_meta before %q", kind),
		}
	}
	return rawEvent{kind: kind, versionKey: p.codexVersion}, nil
}

// extractCLIVersion pulls payload.cli_version off a codex session_meta
// line's payload object. A missing or empty value is UNRESOLVED per the
// ADR: a truncated capture that lost its version signal must fail closed,
// not proceed versionless.
func extractCLIVersion(payload json.RawMessage) (string, error) {
	var body struct {
		CLIVersion string `json:"cli_version"`
	}
	if len(payload) == 0 {
		return "", cascade.New(cascade.KindUnsupported, "codex session_meta missing payload")
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "malformed codex session_meta payload")
	}
	if body.CLIVersion == "" {
		return "", cascade.New(cascade.KindUnsupported, "codex session_meta missing cli_version")
	}
	return body.CLIVersion, nil
}

// isDottedNumericVersion reports whether v looks like the dotted-numeric
// release strings observed in real cc captures (e.g. "2.1.140"): at least
// two dot-separated all-numeric segments. This is deliberately loose
// (it is a shape check, not a semver parser) because the ADR's evidence
// is that "version" is the harness's own release string, not an
// independent schema version, and no snapshot table beyond this shape
// check has real multi-version divergence to justify one yet.
func isDottedNumericVersion(v string) bool {
	if v == "" {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}
