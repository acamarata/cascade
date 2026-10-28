package runtime

// Purpose: the guarded config family registry. A family models every key a
//
//	production decoder reads from one guarded section ([policy], [nodes],
//	[conductor], [agents.egress], ...). Its canonical value rides in
//	EffectiveConfig.Families, so the baseline hash, the hot-reload gate and
//	config.accept_loosening cover it like the built-in fields; ApplyEffective
//	writes the admitted value back into the config consumers read.
//
// Inputs: families registered at init (cmd/cascade/guarded_extractors.go).
// Outputs: Families values, their comparison, fallback and write-back, and
//
//	the dotted keys each family makes known to the config writer.
//
// Constraints: fail closed. An Extract error is the invalid marker, never a
//
//	tightening; a Compare error is a loosening; a section no registered
//	family owns differs only as a loosening. A duplicate or incomplete
//	registration panics.
//
// SPORT: runtime/boot-baseline-check (CHANGED, guarded families).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// GuardedFamily is one guarded section's model.
type GuardedFamily struct {
	// Section is the dotted table name, e.g. "policy" or "agents.egress".
	Section string
	// Keys are the dotted keys the family models; each becomes known to
	// `cascade config set`. A family keyed by harness or lane lists none.
	Keys []string
	// Extract runs the section's production decoder over the whole tree and
	// returns its canonical value; empty, null or {} is the shipped default.
	Extract func(extra map[string]interface{}) (json.RawMessage, error)
	// Compare names every key next loosens relative to base (nil: default).
	Compare func(base, next json.RawMessage) ([]LooseningPath, error)
	// Restrict returns the tighter of the shipped default and value, key by
	// key. Nil: value when Compare finds it no looser than the default.
	Restrict func(value json.RawMessage) (json.RawMessage, error)
	// Apply writes value (nil: the default) into extra, copying each table it
	// changes. Nil when a built-in field writes the values (Extract validates).
	Apply func(extra map[string]interface{}, value json.RawMessage)
}

var guardedFamilyRegistry = struct {
	sync.RWMutex
	bySection map[string]GuardedFamily
}{bySection: map[string]GuardedFamily{}}

// RegisterGuardedFamily installs f at init and adds its Keys to the config
// writer's known keys. An empty Section, a nil Extract or Compare, or a
// Section registered twice panics.
func RegisterGuardedFamily(f GuardedFamily) {
	if f.Section == "" || f.Extract == nil || f.Compare == nil {
		panic(fmt.Sprintf("runtime: guarded family %q needs a Section, an Extract and a Compare", f.Section))
	}
	guardedFamilyRegistry.Lock()
	defer guardedFamilyRegistry.Unlock()
	if _, dup := guardedFamilyRegistry.bySection[f.Section]; dup {
		panic("runtime: guarded family " + f.Section + " registered twice")
	}
	guardedFamilyRegistry.bySection[f.Section] = f
	for _, k := range f.Keys {
		if !slices.Contains(knownConfigKeys, k) {
			knownConfigKeys = append(knownConfigKeys, k)
		}
	}
}

// registeredFamilies returns every registered family, sorted by Section.
func registeredFamilies() []GuardedFamily {
	guardedFamilyRegistry.RLock()
	defer guardedFamilyRegistry.RUnlock()
	out := slices.Collect(maps.Values(guardedFamilyRegistry.bySection))
	slices.SortFunc(out, func(a, b GuardedFamily) int { return strings.Compare(a.Section, b.Section) })
	return out
}

func lookupFamily(section string) (GuardedFamily, bool) {
	guardedFamilyRegistry.RLock()
	defer guardedFamilyRegistry.RUnlock()
	f, ok := guardedFamilyRegistry.bySection[section]
	return f, ok
}

// invalidFamilyValue stands for a section its decoder refused: no Extract
// produces it and every comparison reads it as a loosening.
var invalidFamilyValue = json.RawMessage(`{"!invalid":true}`)

func isInvalidFamily(v json.RawMessage) bool { return bytes.Equal(v, invalidFamilyValue) }

// normaliseFamilyValue compacts raw; the default (empty, null, {}) is nil,
// an error or malformed JSON the invalid marker.
func normaliseFamilyValue(raw json.RawMessage, err error) json.RawMessage {
	var buf bytes.Buffer
	switch t := string(bytes.TrimSpace(raw)); {
	case err != nil:
		return invalidFamilyValue
	case t == "" || t == "null" || t == "{}":
		return nil
	case json.Compact(&buf, raw) != nil:
		return invalidFamilyValue
	}
	return buf.Bytes()
}

// extractFamilies runs every Extract over extra; nil when all are at default,
// so such a config hashes exactly as it did before families existed.
func extractFamilies(extra map[string]interface{}) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range registeredFamilies() {
		if v := normaliseFamilyValue(f.Extract(extra)); v != nil {
			out[f.Section] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// compareFamilies reports every family value next loosens relative to base.
func compareFamilies(base, next map[string]json.RawMessage) []LooseningPath {
	var paths []LooseningPath
	for _, s := range unionKeys(rawTable(base), rawTable(next)) {
		b, n := base[s], next[s]
		if bytes.Equal(b, n) {
			continue
		}
		f, ok := lookupFamily(s)
		reason := "no registered model orders this change; any change is treated as loosening"
		var p []LooseningPath
		var err error
		switch {
		case isInvalidFamily(n):
			reason = "the section's decoder refuses it; it is never read as a tightening"
		case ok && !isInvalidFamily(b):
			if p, err = f.Compare(b, n); err == nil {
				paths = append(paths, p...)
				continue
			}
			reason = "comparison failed (" + err.Error() + "); treated as loosening"
		}
		proposed := string(n)
		if isInvalidFamily(n) {
			proposed = "invalid"
		}
		paths = append(paths, LooseningPath{Family: s, Key: s, Current: string(b), Proposed: proposed, Reason: reason})
	}
	return paths
}

func rawTable(m map[string]json.RawMessage) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// restrictFamilies is the Families a degraded boot runs: per family, the
// tighter of its shipped default and onDisk's value. An invalid value, an
// unregistered section or a failed Restrict falls back to the default.
func restrictFamilies(onDisk map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for s, v := range onDisk {
		f, ok := lookupFamily(s)
		if !ok || isInvalidFamily(v) {
			continue
		}
		var kept json.RawMessage
		if f.Restrict != nil {
			r, err := f.Restrict(v)
			kept = normaliseFamilyValue(r, err)
		} else if p, err := f.Compare(nil, v); err == nil && len(p) == 0 {
			kept = v
		}
		if kept != nil && !isInvalidFamily(kept) {
			out[s] = kept
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyGuardedFamilies writes each family's admitted value back into extra;
// a family absent from fams is written at its default, which removes the
// section's modeled keys. An invalid value stays as the file has it.
func applyGuardedFamilies(extra map[string]interface{}, fams map[string]json.RawMessage) {
	for _, f := range registeredFamilies() {
		if v := fams[f.Section]; f.Apply != nil && !isInvalidFamily(v) {
			f.Apply(extra, v)
		}
	}
}

// applyFamilyPath writes v at the dotted section path, cloning each table
// it changes and creating none for a default (nil) value.
func applyFamilyPath(m map[string]interface{}, path []string, s FieldSpec, v interface{}) {
	if len(path) == 1 {
		applyFamilyKey(m, path[0], s, v)
		return
	}
	child, _ := m[path[0]].(map[string]interface{})
	if child == nil && v == nil {
		return
	}
	c := maps.Clone(child)
	if c == nil {
		c = map[string]interface{}{}
	}
	applyFamilyPath(c, path[1:], s, v)
	m[path[0]] = c
}

func applyFamilyKey(parent map[string]interface{}, key string, s FieldSpec, v interface{}) {
	if s.Rule != RuleTable {
		delete(parent, key)
		if v != nil {
			parent[key] = tomlValue(v)
		}
		return
	}
	existing, _ := parent[key].(map[string]interface{})
	vm, _ := v.(map[string]interface{})
	if existing == nil && len(vm) == 0 {
		return
	}
	t := map[string]interface{}{}
	maps.Copy(t, existing)
	for _, k := range unionKeys(existing, vm) {
		if _, named := s.Fields[k]; named || s.Other != nil {
			applyFamilyKey(t, k, s.sub(k), vm[k])
		}
	}
	parent[key] = t
}

// DefaultSecretsClipboardTTL is the shipped [secrets] clipboard_ttl (08 §3).
const DefaultSecretsClipboardTTL = 30 * time.Second

// invalidClipboardTTL stands for a clipboard_ttl that is not a positive
// duration: the longest one, so it always reads as a raise.
const invalidClipboardTTL = time.Duration(1<<63 - 1)

// compareClipboardTTL reports a raise of secrets.clipboard_ttl (a secret
// stays on the clipboard longer); a lowering is a tightening.
func compareClipboardTTL(current, proposed *time.Duration) []LooseningPath {
	cur, prop := resolveTTL(current), resolveTTL(proposed)
	if prop <= cur {
		return nil
	}
	return []LooseningPath{{Family: "secrets", Key: "secrets.clipboard_ttl", Current: cur.String(), Proposed: prop.String(),
		Reason: "a longer clipboard TTL keeps a secret exposed longer; an absent key is the shipped default"}}
}

func resolveTTL(p *time.Duration) time.Duration {
	if p == nil {
		return DefaultSecretsClipboardTTL
	}
	return *p
}

// SecretsClipboardTTL is the only reader of secrets.clipboard_ttl: the
// admitted value, or the shipped default when absent.
func SecretsClipboardTTL(eff EffectiveConfig) (time.Duration, error) {
	if d := resolveTTL(eff.Secrets.ClipboardTTL); d != invalidClipboardTTL {
		return d, nil
	}
	return 0, fmt.Errorf("runtime: secrets.clipboard_ttl is not a positive duration")
}

// RetrievalEmbeddingLocalSocket is the only reader of
// retrieval.embedding.local_socket: the admitted value, "" when unset.
func RetrievalEmbeddingLocalSocket(eff EffectiveConfig) (string, error) {
	var v struct {
		LocalSocket string `json:"local_socket"`
	}
	raw, ok := eff.Families["retrieval.embedding"]
	if ok && (isInvalidFamily(raw) || json.Unmarshal(raw, &v) != nil) {
		return "", fmt.Errorf("runtime: [retrieval.embedding] is not valid; local_socket is not used")
	}
	return v.LocalSocket, nil
}
