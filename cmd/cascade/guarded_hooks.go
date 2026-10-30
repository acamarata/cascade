// Purpose: the guarded [[hooks]] config family (contract:hooks-vault-binding).
// Its admitted value is the canonical hook list (sorted by id; keys id,
// namespace, trigger, action_type, action_params; params verbatim, so tags,
// never values), so the baseline hash, the hot-reload gate and
// config.accept_loosening cover every hook. An added hook and any change to
// an existing hook's namespace, trigger, action_type or action_params is a
// hooks.action loosening; a new (hook, plugin, vault name) binding is also a
// hooks.vault_binding loosening. Only a removal is a tightening.
// Inputs: the config tree. Outputs: a RegisterGuardedFamily call at init.
// Constraints: one decode (hooks.ParseHooksSection, then hooks.VaultBindingsOf
// over the rehydrate scanner's tags); a section either refuses is an Extract
// error, never an empty list. Apply is how ApplyEffective writes the admitted
// list back as [[hooks]], so a divergent boot runs the admitted hooks.
// SPORT: cmd/cascade guarded hooks family (P1-SUP-05).
package main

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The loosening keys the family reports.
const (
	hooksActionKey  = "hooks.action"
	hooksBindingKey = "hooks.vault_binding"
)

func init() {
	runtime.RegisterGuardedFamily(runtime.GuardedFamily{Section: hooks.SectionName,
		Extract: hooksFamilyExtract, Compare: hooksFamilyCompare, Apply: hooksFamilyApply})
}

// hookLoadable reports the action types the [[hooks]] section may load:
// plugin-call and agent-note. P1-SUP-02 makes its body read the runner
// registrations; any other type refuses the section.
func hookLoadable(t hooks.ActionType) bool {
	return t == hooks.ActionTypePluginCall || t == hooks.ActionTypeAgentNote
}

// hookTagNames is VaultBindingsOf's tag reader: the names the rehydrate
// scanner finds, refusing a pii tag (pii is never bound to a hook).
func hookTagNames(content []byte) ([]string, error) {
	tags, err := secrets.RehydrationTagNames(content)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Type == secrets.TagPII {
			return nil, cascade.New(cascade.KindInvalidInput, "hooks: a pii tag is never bound to a hook")
		}
		names = append(names, tag.Name)
	}
	return names, nil
}

// hooksFamilyExtract returns the canonical hook list; no hooks is the
// shipped default (null). A section that does not parse, or whose
// bindings refuse, is an error.
func hooksFamilyExtract(extra map[string]interface{}) (json.RawMessage, error) {
	list, err := hooks.ParseHooksSection(extra[hooks.SectionName], hookLoadable)
	if err != nil {
		return nil, err
	}
	if _, err := hooks.VaultBindingsOf(list, hookTagNames); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	slices.SortFunc(list, func(a, b hooks.HookConfig) int { return strings.Compare(a.ID, b.ID) })
	return json.Marshal(list)
}

// decodeHookList reads a family value back; empty or null is no hooks.
func decodeHookList(raw json.RawMessage) ([]hooks.HookConfig, error) {
	var list []hooks.HookConfig
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "hooks: the [hooks] family value is not a hook list")
	}
	return list, nil
}

// hooksFamilyCompare names every hook next adds or changes (hooks.action)
// and every binding next adds (hooks.vault_binding). Details name hook
// ids, action types, plugins, tools and vault names, never a param value.
func hooksFamilyCompare(base, next json.RawMessage) ([]runtime.LooseningPath, error) {
	b, err := decodeHookList(base)
	if err != nil {
		return nil, err
	}
	n, err := decodeHookList(next)
	if err != nil {
		return nil, err
	}
	baseBindings, err := hooks.VaultBindingsOf(b, hookTagNames)
	if err != nil {
		return nil, err
	}
	nextBindings, err := hooks.VaultBindingsOf(n, hookTagNames)
	if err != nil {
		return nil, err
	}
	paths := hookActionPaths(b, n)
	for _, bind := range nextBindings {
		if !slices.Contains(baseBindings, bind) {
			paths = append(paths, runtime.LooseningPath{Family: hooks.SectionName, Key: hooksBindingKey,
				Proposed: bind.HookID + "/" + bind.PluginID + "/" + bind.VaultName,
				Reason:   "a new vault binding lets this hook's plugin call receive the named vault value"})
		}
	}
	return paths, nil
}

// hookActionPaths reports each hook of next that base lacks or holds
// differently.
func hookActionPaths(base, next []hooks.HookConfig) []runtime.LooseningPath {
	byID := make(map[string]hooks.HookConfig, len(base))
	for _, h := range base {
		byID[h.ID] = h
	}
	var paths []runtime.LooseningPath
	for _, h := range next {
		old, ok := byID[h.ID]
		if ok && sameHook(old, h) {
			continue
		}
		reason := "a new hook runs an action on an event"
		if ok {
			reason = "the hook's namespace, trigger, action_type or action_params changed"
		}
		paths = append(paths, runtime.LooseningPath{Family: hooks.SectionName, Key: hooksActionKey,
			Proposed: h.ID + "/" + string(h.ActionType) + "/" + h.ActionParams[hooks.PluginParamName] + "/" +
				h.ActionParams[hooks.PluginToolParam],
			Reason: reason})
	}
	return paths
}

func sameHook(a, b hooks.HookConfig) bool {
	return a.Namespace == b.Namespace && a.Trigger == b.Trigger && a.ActionType == b.ActionType &&
		maps.Equal(a.ActionParams, b.ActionParams)
}

// hooksFamilyApply writes the admitted hook list into extra as the
// [[hooks]] array. The default, or a value that does not decode, removes
// the section, so no unadmitted hook survives the write-back.
func hooksFamilyApply(extra map[string]interface{}, value json.RawMessage) {
	list, err := decodeHookList(value)
	if err != nil || len(list) == 0 {
		delete(extra, hooks.SectionName)
		return
	}
	entries := make([]interface{}, 0, len(list))
	for _, h := range list {
		entry := map[string]interface{}{"id": h.ID, "namespace": h.Namespace, "trigger": h.Trigger,
			"action_type": string(h.ActionType)}
		if len(h.ActionParams) > 0 {
			params := make(map[string]interface{}, len(h.ActionParams))
			for k, v := range h.ActionParams {
				params[k] = v
			}
			entry["action_params"] = params
		}
		entries = append(entries, entry)
	}
	extra[hooks.SectionName] = entries
}
