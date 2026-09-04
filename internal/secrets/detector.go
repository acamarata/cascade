// Purpose: the local-only secret detector. Detector.Scan reads a span of
//
//	content and reports WHERE credential material is and WHAT SHAPE it
//	has - never what it says.
//
// Inputs: content bytes, a Registry (registry.go) and a DetectionConfig
//
//	(detection_config.go). Nothing else: no file, no environment, no
//	clock, and - the hard boundary of this ticket - no network. This
//	package imports neither net nor net/http, and internal/build's
//	arch_secrets_test.go proves it for the whole directory.
//
// Outputs: []DetectionHit, sorted by offset, deterministic for a given
//
//	(content, registry, config). No map is ever iterated to produce it.
//
// Constraints: PRECISION-FIRST. Three signals combine - pattern, Shannon
//
//	entropy, and a credential-named field within nameWindowBytes - and
//	entropy ALONE never reaches the quarantine threshold. A DetectionHit
//	has no field that can hold a value, so "the detector leaked what it
//	detected" is a compile error rather than a review finding.
//
// SPORT: SECRETS_DETECTOR: ADD (internal/secrets.Detector, DetectionHit).

package secrets

import (
	"sort"
)

// nameWindowBytes is how far back from a candidate span the context-name
// heuristic looks for a credential-named field. 64 bytes covers
// `AWS_SECRET_ACCESS_KEY = <value>`, a YAML `password:` on the same line,
// and a JSON `"token":` - and stops well short of dragging in an
// unrelated field from two lines up.
const nameWindowBytes = 64

// minEntropyRun is the shortest run the entropy signal considers. Below
// 16 characters a Shannon estimate has too few samples to separate an
// opaque token from an ordinary identifier.
const minEntropyRun = 16

// maxSuggestedNameWords is how many words left of a credential keyword
// are folded into a SuggestedName: enough for WIFI_PASSWORD and
// AWS_SECRET_ACCESS_KEY, short enough that a sentence does not become one.
const maxSuggestedNameWords = 4

// DetectionHit is one located span of suspected credential material.
//
// It has no value field and never will: the whole point of this type is
// that it can be logged, serialised into a quarantine record and printed
// by the CLI without any of those paths acquiring a way to disclose the
// secret. A consumer that needs the bytes re-reads them from the source
// it already holds, using Offset and Len.
type DetectionHit struct {
	// Class is the credential class matched.
	Class Class `json:"class"`
	// Pattern names the registry pattern that fired, or "entropy" for a
	// shape-only signal. Diagnostic only; never a value.
	Pattern string `json:"pattern"`
	// Offset is the byte offset of the span within the scanned content.
	Offset int `json:"offset"`
	// Len is the span's length in bytes.
	Len int `json:"len"`
	// Confidence is the score this hit earned, in [0,1].
	Confidence Confidence `json:"confidence"`
	// SuggestedName is an UPPER_SNAKE vault name derived from the
	// surrounding context, or the class default. Always non-empty and
	// always a name validateSecretName accepts.
	SuggestedName string `json:"suggested_name"`
}

// Detector scans content for credential material. Build one with
// NewDetector; the zero value has no registry and matches nothing.
//
// A Detector is safe for concurrent Scan calls: it holds no mutable
// state, and Reload swaps an immutable config value under a mutex.
type Detector struct {
	registry Registry
	cfg      atomicConfig
}

// NewDetector returns a detector over registry, configured by cfg. An
// invalid cfg is refused rather than silently replaced by the defaults: a
// detector running under a threshold the operator did not ask for is
// exactly the failure this ticket exists to avoid.
func NewDetector(registry Registry, cfg DetectionConfig) (*Detector, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	d := &Detector{registry: registry}
	d.cfg.store(cfg)
	return d, nil
}

// Reload replaces the detector's configuration, for the hot-reload path.
// A rejected config leaves the running configuration untouched, so a bad
// edit degrades to "the old rules still apply", never to "no rules apply".
func (d *Detector) Reload(cfg DetectionConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	d.cfg.store(cfg)
	return nil
}

// Config returns the configuration currently in force.
func (d *Detector) Config() DetectionConfig { return d.cfg.load() }

// Scan reports every span of content that looks like credential
// material. Hits are returned sorted by offset; overlapping candidates
// are resolved so that one span of bytes produces at most one hit.
//
// Scan is pure: same content, same registry, same config, same result,
// in the same order. It performs no I/O of any kind.
func (d *Detector) Scan(content []byte) []DetectionHit {
	if len(content) == 0 {
		return nil
	}
	cfg := d.cfg.load()
	text := string(content)
	hits := d.patternHits(text)
	hits = append(hits, d.entropyHits(text, cfg)...)
	return resolveOverlaps(hits)
}

// ScanCertain returns only the hits that reach the configured
// confidence threshold - the precision-first gate the quarantine writer
// sits behind. Ambiguous hits are dropped here and never persisted.
func (d *Detector) ScanCertain(content []byte) []DetectionHit {
	threshold := Confidence(d.cfg.load().ConfidenceThreshold)
	var out []DetectionHit
	for _, hit := range d.Scan(content) {
		if hit.Confidence >= threshold {
			out = append(out, hit)
		}
	}
	return out
}

// patternHits runs the registry table over text in its fixed order.
func (d *Detector) patternHits(text string) []DetectionHit {
	var hits []DetectionHit
	for _, pattern := range d.registry.Patterns() {
		for _, loc := range pattern.Expr.FindAllStringIndex(text, -1) {
			span := text[loc[0]:loc[1]]
			if pattern.Decode != nil && !pattern.Decode(span) {
				continue
			}
			named, name := contextName(text, loc[0])
			confidence := pattern.Weight
			if named && confidence < ConfidenceCorroborated {
				confidence = ConfidenceCorroborated
			}
			hits = append(hits, DetectionHit{
				Class: pattern.Class, Pattern: pattern.Name,
				Offset: loc[0], Len: loc[1] - loc[0], Confidence: confidence,
				SuggestedName: suggestedName(name, pattern.Class),
			})
		}
	}
	return hits
}

// entropyHits is the shape-only signal. A run reaches
// ConfidenceCorroborated only with a credential-named field beside it,
// and a run whose shape is a common non-secret identifier is capped below
// the threshold whatever its surroundings say - see structuredIdentifier.
func (d *Detector) entropyHits(text string, cfg DetectionConfig) []DetectionHit {
	var hits []DetectionHit
	for _, loc := range tokenRuns(text) {
		span := text[loc[0]:loc[1]]
		if !opaqueCandidate(span) || shannonEntropy(span) < cfg.EntropyFloor {
			continue
		}
		named, name := contextName(text, loc[0])
		confidence := ConfidenceWeak
		switch {
		case structuredIdentifier(span):
			confidence = ConfidenceStructured
		case named:
			confidence = ConfidenceCorroborated
		}
		hits = append(hits, DetectionHit{
			Class: ClassHighEntropy, Pattern: "entropy",
			Offset: loc[0], Len: loc[1] - loc[0], Confidence: confidence,
			SuggestedName: suggestedName(name, ClassHighEntropy),
		})
	}
	return hits
}

// resolveOverlaps keeps, for each region of bytes, the single most
// confident hit. Ordering of the decision is fully specified (confidence,
// then offset, then length, then class, then pattern) so the outcome
// never depends on the order the signals happened to be appended in.
func resolveOverlaps(hits []DetectionHit) []DetectionHit {
	if len(hits) == 0 {
		return nil
	}
	ranked := append([]DetectionHit(nil), hits...)
	sort.Slice(ranked, func(i, j int) bool { return hitBefore(ranked[i], ranked[j]) })
	var kept []DetectionHit
	for _, candidate := range ranked {
		if !overlapsAny(kept, candidate) {
			kept = append(kept, candidate)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Offset != kept[j].Offset {
			return kept[i].Offset < kept[j].Offset
		}
		return hitBefore(kept[i], kept[j])
	})
	return kept
}

// hitBefore is the total order used to rank candidates.
func hitBefore(a, b DetectionHit) bool {
	switch {
	case a.Confidence != b.Confidence:
		return a.Confidence > b.Confidence
	case a.Offset != b.Offset:
		return a.Offset < b.Offset
	case a.Len != b.Len:
		return a.Len > b.Len
	case a.Class != b.Class:
		return a.Class < b.Class
	default:
		return a.Pattern < b.Pattern
	}
}

// overlapsAny reports whether candidate shares any byte with a kept hit.
func overlapsAny(kept []DetectionHit, candidate DetectionHit) bool {
	for _, k := range kept {
		if candidate.Offset < k.Offset+k.Len && k.Offset < candidate.Offset+candidate.Len {
			return true
		}
	}
	return false
}
