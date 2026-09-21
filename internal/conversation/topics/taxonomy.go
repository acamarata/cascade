// Purpose: TopicType (the canonical topic identifier every sub-system in
//   this ticket routes on) and TaxonomyConfig, the label->TopicType mapping
//   mechanism AutoThreader.Route consults to turn a Segmenter's raw
//   classifier Label (segmenter_types.go's Boundary.Label) into one.
// Inputs: a caller-supplied label->TopicType map and fallback TopicType, at
//   construction.
// Outputs: a TopicType from Resolve; never an error - an unmapped label is
//   an expected, common case (R-14.63), not a caller mistake.
// Constraints: MECHANISM ONLY (R-14.63): core ships no built-in label set
//   and no [topics.taxonomy] core-config section - cascade-pa injects its
//   own default labels ('general', 'code', 'memory', 'task') through its
//   own plugin config ([plugins.<name>], 08-INIT-CONFIG-SPEC §3), not
//   through anything in this file. TopicType is therefore deliberately NOT
//   a closed enum like Role or SegmentKind (domain.go): the set of topic
//   types this package will ever see is whatever the caller's labels map
//   and fallback name, decided entirely outside core. Open does not mean
//   unbounded, though: validateTopicType below is the boundary every
//   resolved TopicType crosses before it becomes a storage key or a thread
//   id, and it documents the reachable key space.
// SPORT: internal/conversation/topics taxonomy (ADD) (P1-E21-W5-S45-T3).

package topics

import "github.com/acamarata/cascade/pkg/cascade"

// TopicType is the canonical topic identifier ThreadStore, ExemplarStore,
// and Reassign all key on. Deliberately an open string type, never a
// closed enum: see this file's header for why (R-14.63).
type TopicType string

// TaxonomyConfig resolves a Segmenter's raw classifier label to a
// TopicType. The zero value has a nil Labels map and an empty Fallback,
// which is a valid (if maximally permissive) configuration: every label,
// known or not, resolves to the empty TopicType. Callers that want a real
// fallback pass one to NewTaxonomyConfig.
type TaxonomyConfig struct {
	labels   map[string]TopicType
	fallback TopicType
}

// NewTaxonomyConfig builds a TaxonomyConfig from a caller-supplied
// label->TopicType map and a fallback TopicType for anything the map does
// not name. labels is copied so a caller mutating its own map afterward
// cannot change a TaxonomyConfig already handed to an AutoThreader. A nil
// or empty labels map is accepted (Resolve then always returns fallback) -
// not an error, since a caller with no labels yet (or one relying entirely
// on the fallback) is a legitimate configuration, not a mistake.
func NewTaxonomyConfig(labels map[string]TopicType, fallback TopicType) TaxonomyConfig {
	cp := make(map[string]TopicType, len(labels))
	for k, v := range labels {
		cp[k] = v
	}
	return TaxonomyConfig{labels: cp, fallback: fallback}
}

// topicTypeMaxLen bounds one TopicType's length in bytes. A TopicType
// becomes part of a storage key (exemplarKey) and of a thread id
// (topicThreadIDPrefix), so an unbounded one is an unbounded key.
const topicTypeMaxLen = 64

// validateTopicType is the boundary check every TopicType crosses before it
// reaches a store key: non-empty, within topicTypeMaxLen bytes, and drawn
// from the conservative key-safe alphabet below. Refused with
// KindInvalidInput rather than sanitized, since silently rewriting a
// caller's topic would file turns under a topic the caller never named.
//
// TOTAL KEY SPACE. The alphabet-and-length bound above is only the outer
// limit. The reachable space is far smaller and is set by the caller, not
// by this package: Resolve can only ever return a value from the labels map
// it was constructed with or that config's single fallback, so one
// TaxonomyConfig reaches at most len(labels)+1 distinct TopicTypes, and
// ExemplarStore holds at most maxDepth exemplars under each. Core ships no
// labels at all (R-14.63), so core's own reachable space is exactly one:
// the configured fallback.
func validateTopicType(t TopicType) error {
	if t == "" {
		return cascade.New(cascade.KindInvalidInput, "topics: TopicType must not be empty")
	}
	if len(t) > topicTypeMaxLen {
		return cascade.Newf(cascade.KindInvalidInput,
			"topics: TopicType %q is %d bytes, over the %d-byte bound", t, len(t), topicTypeMaxLen)
	}
	for i := 0; i < len(t); i++ {
		if !topicTypeByteAllowed(t[i]) {
			return cascade.Newf(cascade.KindInvalidInput,
				"topics: TopicType %q carries the disallowed byte %q at index %d: "+
					"allowed are letters, digits, '.', '_', '-' and ':'", t, t[i], i)
		}
	}
	return nil
}

// topicTypeByteAllowed reports whether c may appear in a TopicType. ASCII
// only and deliberately narrow: these bytes are safe in a storage key, a
// thread id, and a CLI column without quoting or escaping anywhere.
func topicTypeByteAllowed(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '_', c == '-', c == ':':
		return true
	}
	return false
}

// Resolve maps label to its configured TopicType, or Fallback for any
// label not present in the configured map - including the empty string,
// which is never a special case here: an empty label is simply a label
// this TaxonomyConfig was not given a mapping for. Resolve never panics
// and never errors: a caller-supplied label the taxonomy cannot place is
// the expected common case (R-14.63), not a caller mistake to refuse.
func (c TaxonomyConfig) Resolve(label string) TopicType {
	if t, ok := c.labels[label]; ok {
		return t
	}
	return c.fallback
}
