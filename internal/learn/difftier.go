package learn

// Purpose: DiffTier, the stored-versus-computed tier check over the change
//   materialised against the current config value.
// Inputs: a ConfigValueReader and one stored LearnedConfig.
// Outputs: a TierDiff: the recomputed classification, the stored tier,
//   whether they disagree, whether the change is held and why, the
//   materialised value and the file digest it was computed against.
// Constraints: the computed tier always comes from ClassifyChange, never
//   from c.Tier. A read error is never absence: it holds the change as
//   security with the error kept unchanged. Steps run in order and stop at
//   the first hold.
// SPORT: internal.learn.DiffTier/ADDED, internal.learn.TierDiff/ADDED
//   (P1-LRN-03).

import (
	"slices"
	"strconv"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The hold reasons DiffTier reports.
const (
	// HoldAbsentListReplacesRepoDefaults: an add_step on a list the file
	// does not set would replace the repo's default steps, not extend them.
	HoldAbsentListReplacesRepoDefaults = "absent_list_replaces_repo_defaults"
	// HoldEffectiveSourceNotFile: an env var or flag overrides the key, so
	// a file edit would not change the effective value.
	HoldEffectiveSourceNotFile = "effective_source_not_file"
)

// The hold reasons that also force the computed tier to security.
const (
	// HoldReadError: the reader failed or is missing.
	HoldReadError = "read_error"
	// HoldUnmaterialisable: the change cannot be classified or applied to
	// the current value.
	HoldUnmaterialisable = "unmaterialisable"
)

// TierDiff is DiffTier's verdict. Computed is ClassifyChange's result
// (security on any hold that blocks materialising); Stored is the row's
// own tier and Mismatch reports Stored != Computed.Tier. Materialised is
// the TOML literal the key would hold after the change.
type TierDiff struct {
	Computed                             Classification
	Stored                               ConfigTier
	Mismatch, Held                       bool
	HoldReason, Materialised, FileDigest string
	Err                                  error
}

// DiffTier recomputes c's tier with ClassifyChange and materialises
// c.Change against the value r reads for c.Change.Path. It never returns
// the stored tier as the computed one and never reads an error as absence.
func DiffTier(r ConfigValueReader, c LearnedConfig) TierDiff {
	d := diffTier(r, c)
	d.Stored = c.Tier
	d.Mismatch = d.Stored != d.Computed.Tier
	return d
}

// diffTier runs the steps; DiffTier fills Stored and Mismatch.
func diffTier(r ConfigValueReader, c LearnedConfig) TierDiff {
	cls, err := ClassifyChange(string(c.Target), c.Change)
	if err != nil {
		cls = Classification{Target: c.Target, Paths: []FieldPath{c.Change.Path}, Tier: TierSecurity}
		return TierDiff{Computed: cls, Held: true, HoldReason: HoldUnmaterialisable, Err: err}
	}
	if r == nil {
		err = cascade.New(cascade.KindUnavailable, "learn: tier diff has no config reader")
		return securityHold(cls, HoldReadError, "", err)
	}
	raw, present, source, digest, err := r.Get(string(c.Change.Path))
	if err != nil {
		return securityHold(cls, HoldReadError, digest, err)
	}
	d := TierDiff{Computed: cls, FileDigest: digest}
	switch c.Change.Op {
	case OpAddStep:
		if !present {
			d.Held, d.HoldReason = true, HoldAbsentListReplacesRepoDefaults
			return d
		}
		m, ok := materialiseAddStep(raw, c.Change.Value)
		if !ok {
			return securityHold(cls, HoldUnmaterialisable, digest, nil)
		}
		d.Materialised = m
	case OpReorder:
		if !present || !sameMultiset(raw, c.Change.Value) {
			return securityHold(cls, HoldUnmaterialisable, digest, nil)
		}
		d.Materialised = c.Change.Value
	case OpSet:
		d.Materialised = c.Change.Value
	default:
		d.Materialised = c.Change.Value
	}
	if source == runtime.SourceEnv || source == runtime.SourceFlag {
		d.Held, d.HoldReason = true, HoldEffectiveSourceNotFile
	}
	return d
}

// securityHold holds cls as security for reason.
func securityHold(cls Classification, reason, digest string, err error) TierDiff {
	cls.Tier = TierSecurity
	return TierDiff{Computed: cls, Held: true, HoldReason: reason, FileDigest: digest, Err: err}
}

// materialiseAddStep is raw when value is already listed, else raw with
// value appended. ok is false when raw is not a string array or value is
// not a string literal.
func materialiseAddStep(raw, value string) (string, bool) {
	list, ok := parseStringArray(raw)
	if !ok {
		return "", false
	}
	step, ok := parseStringLiteral(value)
	if !ok {
		return "", false
	}
	if slices.Contains(list, step) {
		return raw, true
	}
	return quoteArray(append(list, step)), true
}

// sameMultiset reports whether raw and value are string arrays holding the
// same elements with the same counts.
func sameMultiset(raw, value string) bool {
	a, okA := parseStringArray(raw)
	b, okB := parseStringArray(value)
	if !okA || !okB || len(a) != len(b) {
		return false
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// parseStringArray decodes a TOML array literal whose elements are all
// strings.
func parseStringArray(literal string) ([]string, bool) {
	var doc struct{ V []any }
	if err := toml.Unmarshal([]byte("V = "+literal), &doc); err != nil || doc.V == nil {
		return nil, false
	}
	out := make([]string, len(doc.V))
	for i, e := range doc.V {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// parseStringLiteral decodes a TOML string literal.
func parseStringLiteral(literal string) (string, bool) {
	var doc struct{ V any }
	if err := toml.Unmarshal([]byte("V = "+literal), &doc); err != nil {
		return "", false
	}
	s, ok := doc.V.(string)
	return s, ok
}

// quoteArray renders list in RuntimeConfigReader's array form.
func quoteArray(list []string) string {
	out := "["
	for i, s := range list {
		if i > 0 {
			out += ", "
		}
		out += strconv.Quote(s)
	}
	return out + "]"
}
