package hooks

import (
	"regexp"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the strict [[hooks]] section parser. The composition root hands
//
//	it the decoded section value and the dispatcher's Runnable predicate;
//	it returns every entry as a HookConfig or refuses the whole section.
//
// Inputs: raw, the decoded [[hooks]] value (an array of tables, as either
//
//	[]any or []map[string]any), and runnable, which decides whether an
//	action type can run on the dispatcher that will load the result.
//
// Outputs: []HookConfig with every ID set (derived when absent), or one
//
//	KindInvalidInput error naming the entry index and key.
//
// Constraints: all or nothing. A parser that skipped a bad entry and loaded
//
//	the rest would silently drop an operator's hook, or load a set the
//	operator never wrote. Error text names indices, keys and types only,
//	never a configured value, because a value may hold a credential.

// SectionName is the config.toml section this parser reads.
const SectionName = "hooks"

// AuditNamespace is the bus namespace HookFire records are published to.
// No hook may listen on it: a hook firing on its own audit trail is the
// most direct loop there is.
const AuditNamespace = "hooks"

// namespacePattern is the shape a hook namespace must have.
var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// hookKeys is the exact key set a [[hooks]] entry may carry.
var hookKeys = map[string]bool{
	"id": true, "namespace": true, "trigger": true, "action_type": true, "action_params": true,
}

// ParseHooksSection parses the [[hooks]] value. nil means the section is
// absent and returns (nil, nil). Any unknown key, wrong type, empty or
// malformed namespace, the audit namespace, an empty trigger, the audit
// event kind as a trigger, an action type runnable refuses, or a duplicate
// id refuses the whole section.
func ParseHooksSection(raw any, runnable func(ActionType) bool) ([]HookConfig, error) {
	if raw == nil {
		return nil, nil
	}
	if runnable == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "hooks: parse: a runnable predicate is required")
	}
	entries, err := sectionEntries(raw)
	if err != nil {
		return nil, err
	}
	out := make([]HookConfig, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		cfg, err := parseHookEntry(i, entry, runnable)
		if err != nil {
			return nil, err
		}
		if seen[cfg.ID] {
			return nil, entryError(i, "id", "duplicates the id of an earlier entry")
		}
		seen[cfg.ID] = true
		out = append(out, cfg)
	}
	return out, nil
}

// sectionEntries normalises the two array shapes TOML decoders produce.
func sectionEntries(raw any) ([]any, error) {
	switch v := raw.(type) {
	case []any:
		return v, nil
	case []map[string]any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out, nil
	default:
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"hooks: [[%s]] must be an array of tables, got %T", SectionName, raw)
	}
}

// parseHookEntry validates one entry. Keys are checked in sorted order so
// the error for a multiply-broken entry is deterministic.
func parseHookEntry(i int, entry any, runnable func(ActionType) bool) (HookConfig, error) {
	m, ok := entry.(map[string]any)
	if !ok {
		return HookConfig{}, entryError(i, "", "must be a table")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !hookKeys[k] {
			return HookConfig{}, entryError(i, k, "is not a known key")
		}
	}
	var cfg HookConfig
	var err error
	if cfg.Namespace, err = requiredString(i, m, "namespace"); err != nil {
		return HookConfig{}, err
	}
	if problem := namespaceProblem(cfg.Namespace); problem != "" {
		return HookConfig{}, entryError(i, "namespace", problem)
	}
	if cfg.Trigger, err = requiredString(i, m, "trigger"); err != nil {
		return HookConfig{}, err
	}
	if cfg.Trigger == string(EventKindHookFire) {
		return HookConfig{}, entryError(i, "trigger", "names the hooks audit event kind")
	}
	actionType, err := requiredString(i, m, "action_type")
	if err != nil {
		return HookConfig{}, err
	}
	if cfg.ActionType = ActionType(actionType); !runnable(cfg.ActionType) {
		return HookConfig{}, entryError(i, "action_type", "is not runnable on this dispatcher")
	}
	if cfg.ActionParams, err = optionalParams(i, m); err != nil {
		return HookConfig{}, err
	}
	return withID(i, m, cfg)
}

// withID sets cfg.ID from the entry, or derives it when absent.
func withID(i int, m map[string]any, cfg HookConfig) (HookConfig, error) {
	if _, present := m["id"]; !present {
		cfg.ID = DeriveHookID(cfg.Namespace, cfg.Trigger, cfg.ActionType, cfg.ActionParams)
		return cfg, nil
	}
	id, err := requiredString(i, m, "id")
	if err != nil {
		return HookConfig{}, err
	}
	cfg.ID = id
	return cfg, nil
}

// requiredString returns m[key] as a non-empty string.
func requiredString(i int, m map[string]any, key string) (string, error) {
	v, present := m[key]
	if !present {
		return "", entryError(i, key, "is required")
	}
	s, ok := v.(string)
	if !ok {
		return "", entryError(i, key, "must be a string")
	}
	if s == "" {
		return "", entryError(i, key, "must not be empty")
	}
	return s, nil
}

// optionalParams returns action_params as a string map, or nil if absent.
func optionalParams(i int, m map[string]any) (map[string]string, error) {
	v, present := m["action_params"]
	if !present {
		return nil, nil
	}
	switch p := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(p))
		for k, s := range p {
			out[k] = s
		}
		return out, nil
	case map[string]any:
		out := make(map[string]string, len(p))
		for k, raw := range p {
			s, ok := raw.(string)
			if !ok {
				return nil, entryError(i, "action_params", "value for a param key must be a string")
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, entryError(i, "action_params", "must be a table of strings")
	}
}

// namespaceProblem returns why ns may not be a hook namespace, or "" when
// it may. It is shared by the parser and Registry.Register.
func namespaceProblem(ns string) string {
	switch {
	case ns == "":
		return "must not be empty"
	case !namespacePattern.MatchString(ns):
		return "must match " + namespacePattern.String()
	case ns == AuditNamespace:
		return "is the hooks audit namespace"
	}
	return ""
}

// entryError is the section refusal for entry i, naming key when set.
func entryError(i int, key, reason string) error {
	if key == "" {
		return cascade.Newf(cascade.KindInvalidInput, "hooks: [[%s]] entry %d %s", SectionName, i, reason)
	}
	return cascade.Newf(cascade.KindInvalidInput, "hooks: [[%s]] entry %d key %q %s", SectionName, i, key, reason)
}
