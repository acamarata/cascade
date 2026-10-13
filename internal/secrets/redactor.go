// Purpose: a read-once snapshot of the vault for the audit log's substitution
//
//	pass. An audit record's `explain` field is JSON, and JSON writes a
//	quote, a backslash, `<`, `>`, `&` and control characters as escapes, so a
//	stored value containing one never appears literally in the bytes the
//	exact-value pass searches; JSON text carried inside a string, or quoted
//	in an action, is escaped again. The snapshot finds every occurrence of
//	every value as stored and escaped up to three levels the way
//	encoding/json writes them, with and without HTML escaping, and the
//	caller rewrites them all in ONE call, so the rewriter's largest-span-first
//	rule decides every overlap (a vault value that embeds another value's
//	escaped form is tagged whole, not cut by the shorter one). Custody is
//	read once.
//	Escapes encoding/json does not write (`\/`, `\u00e4` for a non-ASCII
//	letter, `\u0022` for a quote) are not covered: a stated gap.
//
// Inputs: any source with List and Get (the egress vault).
// Outputs: a SnapshotVault, which is itself a List/Get value source.
// Constraints: fail closed. A List or Get error returns no snapshot. Values
//
//	live in memory for the length of one redaction and are never logged.
//	Escaped forms exist only for values the exact pass would match anyway
//	(8 bytes or longer as stored).
//
// SPORT: AUDIT_REDACTION: ADD (internal/secrets.SnapshotVault).

package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// exactValueFloor mirrors the exact-value pass's shortest matched value
// (internal/hooks/egress minExactValueBytes).
const exactValueFloor = 8

// valueSource is the read side of a vault.
type valueSource interface {
	List(ctx context.Context) ([]string, error)
	Get(ctx context.Context, name string) ([]byte, error)
}

// SnapshotVault is a vault read once: its names and values in memory.
type SnapshotVault struct {
	names  []string
	values map[string][]byte
}

// NewSnapshotVault reads every name and value from src. Any error is
// returned with no snapshot.
func NewSnapshotVault(ctx context.Context, src valueSource) (*SnapshotVault, error) {
	names, err := src.List(ctx)
	if err != nil {
		return nil, err
	}
	snap := &SnapshotVault{names: append([]string(nil), names...), values: make(map[string][]byte, len(names))}
	sort.Strings(snap.names)
	for _, name := range snap.names {
		value, gerr := src.Get(ctx, name)
		if gerr != nil {
			return nil, gerr
		}
		snap.values[name] = value
	}
	return snap, nil
}

// List returns the snapshot's names.
func (s *SnapshotVault) List(context.Context) ([]string, error) {
	return append([]string(nil), s.names...), nil
}

// Get returns one snapshot value, or an ErrSecretNotFound error.
func (s *SnapshotVault) Get(_ context.Context, name string) ([]byte, error) {
	value, ok := s.values[name]
	if !ok {
		return nil, ErrSecretNotFound(name)
	}
	return value, nil
}

// JSONForms returns the snapshots of the JSON string-escaped forms of this
// one's values, deepest first: depth levels of escaping, each level written
// with HTML escaping on and off like encoding/json does (a value holding
// none of < > & has one form per level). Level two is the form a value takes
// inside JSON text that is itself carried in a JSON string, and so on. A
// snapshot holds only the values whose form changed at that step, so a value
// that needs no escaping adds nothing, and a value shorter than the
// exact-value floor as stored adds nothing either: the exact pass would not
// match its stored form, so it must not match an escaped one.
func (s *SnapshotVault) JSONForms(depth int) []*SnapshotVault {
	var out []*SnapshotVault
	level := []*SnapshotVault{s}
	for d := 0; d < depth; d++ {
		var next []*SnapshotVault
		for _, parent := range level {
			next = appendDistinct(next, parent.escaped(true))
			next = appendDistinct(next, parent.escaped(false))
		}
		out = append(next, out...)
		level = next
	}
	return out
}

// escaped returns the snapshot of this one's values that change when JSON
// escaped, or nil when none does.
func (s *SnapshotVault) escaped(escapeHTML bool) *SnapshotVault {
	out := &SnapshotVault{values: map[string][]byte{}}
	for _, name := range s.names {
		raw := s.values[name]
		if len(raw) < exactValueFloor {
			continue
		}
		if form := jsonEscape(raw, escapeHTML); !bytes.Equal(form, raw) {
			out.names = append(out.names, name)
			out.values[name] = form
		}
	}
	if len(out.names) == 0 {
		return nil
	}
	return out
}

// appendDistinct appends snap unless it is nil or equals one already in list.
func appendDistinct(list []*SnapshotVault, snap *SnapshotVault) []*SnapshotVault {
	if snap == nil {
		return list
	}
	for _, have := range list {
		if have.equal(snap) {
			return list
		}
	}
	return append(list, snap)
}

// equal reports whether both snapshots hold the same names and values.
func (s *SnapshotVault) equal(o *SnapshotVault) bool {
	if len(s.names) != len(o.names) {
		return false
	}
	for _, name := range s.names {
		if got, ok := o.values[name]; !ok || !bytes.Equal(got, s.values[name]) {
			return false
		}
	}
	return true
}

// ExactHits returns a detection hit for every occurrence in text of every
// value in the snapshot, as stored and in each JSON-escaped form up to depth
// levels. Hits carry the api-key class, full confidence and the value's
// UPPER_SNAKE reference name, exactly as the egress exact-value pass labels
// them; pass them all to one Rewriter.Rewrite so overlaps resolve
// largest-first across values and forms.
func (s *SnapshotVault) ExactHits(text []byte, depth int) []DetectionHit {
	var hits []DetectionHit
	for _, snap := range append([]*SnapshotVault{s}, s.JSONForms(depth)...) {
		for _, name := range snap.names {
			value := snap.values[name]
			if len(value) < exactValueFloor {
				continue
			}
			for offset := 0; offset < len(text); {
				idx := bytes.Index(text[offset:], value)
				if idx < 0 {
					break
				}
				hits = append(hits, DetectionHit{
					Class: ClassAPIKey, Pattern: "vault-exact", Offset: offset + idx, Len: len(value),
					Confidence: ConfidenceProven, SuggestedName: vaultRefName(name),
				})
				offset += idx + len(value)
			}
		}
	}
	return hits
}

// vaultRefName normalises a stored name into the UPPER_SNAKE reference the
// tag grammar accepts. It mirrors internal/hooks/egress vaultRefName, which
// this package cannot import; a cmd/cascade test pins the two together.
func vaultRefName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out[0] < 'A' || out[0] > 'Z' {
		return "V_" + out
	}
	return out
}

// jsonEscape returns raw as the inside of a JSON string literal.
func jsonEscape(raw []byte, escapeHTML bool) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(string(raw)); err != nil {
		return raw
	}
	encoded := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return encoded[1 : len(encoded)-1]
}
