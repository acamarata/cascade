package runtime

// Purpose: the canonical forms behind the baseline: ComputeSectionsHash,
// MostRestrictiveDefaults (split out of baseline.go per R-14.117/Art.10.3), and
// FieldSpec, the declarative rules a guarded family's canonical value is
// compared, restricted and written back by (guarded_families.go).
// Inputs: an EffectiveConfig; a family's canonical JSON values.
// Outputs: a canonical SHA-256 hex digest; the fail-closed floor config;
// LooseningPath values, the tighter-of-default value, and the TOML write.
// Constraints: deterministic, no I/O, no clock. A family value mirrors its TOML
// table (same key names, lists, tables, integers), so writing it back is
// replacing the modeled keys. A value of an unexpected shape compares as a
// loosening.
// SPORT: runtime/boot-baseline-check (ADD, placeholder per T-8 sport_updates).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// ComputeSectionsHash is the canonical SHA-256 over cfg's six guarded sections,
// each JSON-marshaled (sorted map keys, fixed struct field order) and
// concatenated in baselineGuardedSections' order, then the off-default families.
func ComputeSectionsHash(cfg EffectiveConfig) string {
	h := sha256.New()
	sections := map[string]interface{}{
		"policy":    cfg.Policy,
		"secrets":   cfg.Secrets,
		"sync":      sortedSyncClasses(cfg.Sync),
		"nodes":     cfg.Nodes,
		"conductor": cfg.Conductor,
		"elevation": cfg.Elevation,
	}
	for _, name := range baselineGuardedSections {
		b, err := json.Marshal(sections[name])
		if err != nil {
			// A section that cannot be encoded (an invalid [secrets] threshold)
			// hashes as a marker no valid section encodes to, so it can never
			// equal a recorded hash.
			b = []byte("!unencodable:" + err.Error())
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	// Families are hashed only when one is off its shipped default, so a
	// config whose families are all at default hashes exactly as before.
	if len(cfg.Families) > 0 {
		b, err := json.Marshal(cfg.Families)
		if err != nil {
			b = []byte("!unencodable:" + err.Error())
		}
		h.Write([]byte("families"))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sortedSyncClasses renders SyncSection.Classes as domain-sorted pairs, so the
// order does not depend on encoding/json sorting map keys.
func sortedSyncClasses(s SyncSection) []struct{ Domain, Class string } {
	domains := make([]string, 0, len(s.Classes))
	for d := range s.Classes {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	out := make([]struct{ Domain, Class string }, 0, len(domains))
	for _, d := range domains {
		out = append(out, struct{ Domain, Class string }{Domain: d, Class: s.Classes[d]})
	}
	return out
}

// MostRestrictiveDefaults is the fail-closed floor for a missing or divergent
// baseline (§D-39): elevation.allow_remote=false (08's documented default);
// other built-in fields keep their zero value (Art.1: never invent a default),
// read as "not configured", never loosest. The [secrets] values are absent
// (nil, the shipped default) and Families is nil (each family's absent value).
func MostRestrictiveDefaults() EffectiveConfig {
	return EffectiveConfig{
		Sync:      SyncSection{Classes: map[string]string{}},
		Elevation: elevationSection{AllowRemote: false, HelperPubkey: ""},
	}
}

// FieldRule is how one modeled key of a family value orders its values.
type FieldRule int

// The rules. Absent is the shipped default on either side of a comparison.
const (
	RuleAnyChange   FieldRule = iota // any difference loosens (no ratified order)
	RuleSetGrows                     // a list read as a set: a new entry loosens (allow lists)
	RuleSetShrinks                   // a list read as a set: a removed entry loosens (deny lists)
	RuleSetOrChange                  // setting or changing loosens, unsetting tightens
	RuleRank                         // Ranks order the values tightest first; absent is Ranks[0]
	RuleTable                        // a table: Fields name its keys, Other covers the rest
	RuleCap                          // a non-negative integer limit: absent is no limit, higher loosens
)

// FieldSpec is the rule for one key; a family's top-level spec is a RuleTable.
// An unranked value, or a value of an unexpected shape, compares as looser.
type FieldSpec struct {
	Rule   FieldRule
	Ranks  []interface{}
	Fields map[string]FieldSpec
	// Other is the rule for a key Fields does not name. Nil: such a key is
	// not modeled, so it compares as any change and is never written back.
	Other *FieldSpec
	// Keep lists the entries of a list value a degraded boot keeps: each is
	// no looser than the fallback runs without it. Other entries are dropped.
	Keep []interface{}
}

// Bind fills f's Compare, Restrict and Apply from s.
func (s FieldSpec) Bind(f GuardedFamily) GuardedFamily {
	section := f.Section
	f.Compare = func(base, next json.RawMessage) ([]LooseningPath, error) {
		b, errB := decodeFamilyValue(base)
		n, errN := decodeFamilyValue(next)
		if errB != nil || errN != nil {
			return nil, fmt.Errorf("runtime: %s: undecodable family value", section)
		}
		return s.compare(section, section, b, n), nil
	}
	f.Restrict = func(value json.RawMessage) (json.RawMessage, error) {
		v, err := decodeFamilyValue(value)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s.restrict(v))
	}
	f.Apply = func(extra map[string]interface{}, value json.RawMessage) {
		v, err := decodeFamilyValue(value)
		if err != nil {
			v = nil // a value this family cannot read is written as the default
		}
		applyFamilyPath(extra, strings.Split(section, "."), s, v)
	}
	return f
}

func decodeFamilyValue(raw json.RawMessage) (interface{}, error) {
	var v interface{}
	if len(raw) == 0 {
		return v, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return v, dec.Decode(&v)
}

func (s FieldSpec) compare(family, key string, b, n interface{}) []LooseningPath {
	looser := !reflect.DeepEqual(b, n)
	switch s.Rule {
	case RuleTable:
		bm, bok := b.(map[string]interface{})
		nm, nok := n.(map[string]interface{})
		if (b == nil || bok) && (n == nil || nok) {
			var paths []LooseningPath
			for _, k := range unionKeys(bm, nm) {
				paths = append(paths, s.sub(k).compare(family, key+"."+k, bm[k], nm[k])...)
			}
			return paths
		}
	case RuleSetGrows:
		looser = len(setMinus(n, b)) > 0
	case RuleSetShrinks:
		looser = len(setMinus(b, n)) > 0
	case RuleSetOrChange:
		looser = looser && n != nil
	case RuleRank:
		looser = s.rank(n) > s.rank(b)
	case RuleCap:
		nv, nok := capValue(n)
		bv, bok := capValue(b)
		looser = looser && (n == nil || !nok || (b != nil && (!bok || nv > bv)))
	case RuleAnyChange: // any difference (or a table of an unexpected shape)
	}
	if !looser {
		return nil
	}
	return []LooseningPath{{Family: family, Key: key, Current: b, Proposed: n,
		Reason: fmt.Sprintf("guarded family rule %d orders this change as a loosening; an absent key is the shipped default", s.Rule)}}
}

// sub is the rule for key k of a table.
func (s FieldSpec) sub(k string) FieldSpec {
	if f, ok := s.Fields[k]; ok {
		return f
	}
	if s.Other != nil {
		return *s.Other
	}
	return FieldSpec{Rule: RuleAnyChange}
}

func (s FieldSpec) rank(v interface{}) int {
	if v == nil {
		return 0
	}
	for i, r := range s.Ranks {
		if v == r {
			return i
		}
	}
	return len(s.Ranks)
}

// restrict is the tighter of the shipped default (absent) and v, key by key.
func (s FieldSpec) restrict(v interface{}) interface{} {
	switch m, _ := v.(map[string]interface{}); {
	case s.Rule == RuleTable && m != nil:
		out := map[string]interface{}{}
		for k, x := range m {
			if r := s.sub(k).restrict(x); r != nil {
				out[k] = r
			}
		}
		if len(out) > 0 {
			return out
		}
	case s.Rule == RuleSetShrinks, s.Rule == RuleRank && s.rank(v) == 0:
		return v // the union with the empty default; a rank no looser than it
	case s.Rule == RuleCap:
		if _, ok := capValue(v); ok {
			return v // any limit is tighter than none
		}
	case len(s.Keep) > 0:
		if kept := slices.DeleteFunc(slices.Clone(asList(v)), func(x interface{}) bool { return !slices.Contains(s.Keep, x) }); len(kept) > 0 {
			return kept
		}
	}
	return nil
}

// capValue reads a RuleCap value: a non-negative integer.
func capValue(v interface{}) (int64, bool) {
	n, ok := v.(json.Number)
	i, err := n.Int64()
	return i, ok && err == nil && i >= 0
}

// setMinus returns a's entries missing from b; a non-list a is one entry.
func setMinus(a, b interface{}) []string {
	have := map[string]bool{}
	for _, x := range asList(b) {
		have[fmt.Sprint(x)] = true
	}
	var out []string
	for _, x := range asList(a) {
		if !have[fmt.Sprint(x)] {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func asList(v interface{}) []interface{} {
	if l, ok := v.([]interface{}); ok || v == nil {
		return l
	}
	return []interface{}{v}
}

// tomlValue converts a decoded family value to TOML decoder types (int64 or float64, lists, tables).
func tomlValue(v interface{}) interface{} {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, e := range x {
			out[i] = tomlValue(e)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, e := range x {
			out[k] = tomlValue(e)
		}
		return out
	}
	return v
}
