package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: hook vault bindings (contract:hooks-vault-binding). A
//
//	plugin-call hook whose action_params carry a typed tag binds (hook,
//	plugin, vault NAME). The guarded [hooks] config family makes every new
//	binding a hooks.vault_binding loosening; BoundRehydrate is the
//	dispatch-time seam that lets a fire rehydrate only the names an
//	admitted binding gives it.
//
// Inputs: hook lists and the rehydrate scanner's tag reader
//
//	(VaultBindingsOf); the admitted bindings and a RehydrateBytes over the
//	secrets rehydrator (BoundRehydrate).
//
// Outputs: sorted bindings, or a refusal; the seam's plain params.
//
// Constraints: one scanner. This file never looks for tags itself: the
//
//	tag reader is the rehydrator's own span walk, and the binding check is
//	the allowName predicate the rehydrator asks about each span it found,
//	before any vault read. A tag outside the admitted bindings refuses the
//	whole fire; an admitted source that errors refuses, never permits.
//
// SPORT: contract:hooks-vault-binding (P1-SUP-05).

// VaultBinding is one (hook, plugin, vault NAME) triple: hook HookID, when
// it calls plugin PluginID, may receive the vault entry VaultName.
// VaultBindingsOf returns them sorted by (HookID, PluginID, VaultName).
type VaultBinding struct {
	HookID, PluginID, VaultName string
}

// VaultBindingsOf returns every binding hooks declare, sorted and
// de-duplicated. tags is the rehydrate scanner's reader (in production
// secrets.RehydrationTagNames, refusing a pii tag). A tag in any action
// type but plugin-call, a tag in a param name or in the plugin param, a
// tagged plugin-call naming no plugin, and any tags error (a pii or
// malformed tag) refuse the whole section with KindInvalidInput.
func VaultBindingsOf(hooks []HookConfig, tags func([]byte) ([]string, error)) ([]VaultBinding, error) {
	if tags == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "hooks: no tag reader for the vault bindings")
	}
	var out []VaultBinding
	for _, h := range hooks {
		bound, err := hookBindings(h, tags)
		if err != nil {
			return nil, err
		}
		out = append(out, bound...)
	}
	slices.SortFunc(out, compareBindings)
	return slices.Compact(out), nil
}

// hookBindings returns the bindings one hook's params declare.
func hookBindings(h HookConfig, tags func([]byte) ([]string, error)) ([]VaultBinding, error) {
	var out []VaultBinding
	for _, key := range paramKeys(h.ActionParams) {
		for i, text := range []string{key, h.ActionParams[key]} {
			names, err := tags([]byte(text))
			if err == nil && len(names) == 0 {
				continue
			}
			if i == 0 { // a param name is never quoted: it may be the malformed tag itself
				return nil, cascade.Newf(cascade.KindInvalidInput, "hooks: hook %q has a param name carrying a vault tag", h.ID)
			}
			if err != nil { // the reader's own text can quote a malformed tag's body, so it is dropped
				return nil, cascade.Newf(cascade.KindInvalidInput, "hooks: hook %q param %q carries a malformed or pii vault tag", h.ID, key)
			}
			if reason := bindingProblem(h, key); reason != "" {
				return nil, cascade.Newf(cascade.KindInvalidInput, "hooks: hook %q param %q %s", h.ID, key, reason)
			}
			for _, name := range names {
				out = append(out, VaultBinding{HookID: h.ID, PluginID: h.ActionParams[PluginParamName], VaultName: name})
			}
		}
	}
	return out, nil
}

// bindingProblem names why a tag in this param may never bind, or "".
func bindingProblem(h HookConfig, key string) string {
	switch {
	case h.ActionType != ActionTypePluginCall:
		return "carries a vault tag; only a plugin-call hook binds a vault name"
	case key == PluginParamName:
		return "names its plugin with a vault tag"
	case h.ActionParams[PluginParamName] == "":
		return "carries a vault tag and names no plugin"
	}
	return ""
}

func compareBindings(a, b VaultBinding) int {
	if c := strings.Compare(a.HookID, b.HookID); c != 0 {
		return c
	}
	if c := strings.Compare(a.PluginID, b.PluginID); c != 0 {
		return c
	}
	return strings.Compare(a.VaultName, b.VaultName)
}

// RehydrateBytes resolves the tags in tagged, asking allowName about each
// tag NAME the rehydrator's own scan found before any vault read; in
// production it is secrets.Rehydrator.RehydrateOnly. zero wipes plain.
type RehydrateBytes func(ctx context.Context, tagged []byte, allowName func(name string) bool) (plain []byte, zero func(), err error)

// BoundRehydrate is the rehydration seam that resolves only the names an
// admitted binding gives (fire.Hook.ID, tagged["plugin"]). The tagged
// params go to rehydrate as one canonical JSON object; content with no tag
// comes back byte-identical and the params are returned unchanged, without
// consulting admitted. admitted is read only when a tag is asked about,
// and its error refuses the fire. The returned zero wipes the plain
// buffer; the decoded strings are Go copies outside its reach.
func BoundRehydrate(admitted func() ([]VaultBinding, error), rehydrate RehydrateBytes) RehydrateFunc {
	return func(ctx context.Context, fire Fire, tagged map[string]string) (map[string]string, func(), error) {
		noop := func() {}
		if admitted == nil || rehydrate == nil {
			return nil, noop, cascade.New(cascade.KindUnavailable, "hooks: no bound rehydrator configured")
		}
		encoded, err := encodeTagged(tagged)
		if err != nil {
			return nil, noop, err
		}
		gate := &bindingGate{admitted: admitted, hookID: fire.Hook.ID, pluginID: tagged[PluginParamName]}
		plain, zero, err := rehydrate(ctx, encoded, gate.allow)
		if zero == nil {
			zero = noop
		}
		if err != nil {
			zero()
			return nil, noop, gate.refusal(err)
		}
		if bytes.Equal(plain, encoded) {
			zero()
			return tagged, noop, nil
		}
		out, err := decodePlain(plain, tagged)
		if err != nil {
			zero()
			return nil, noop, err
		}
		return out, zero, nil
	}
}

// bindingGate is the allowName predicate of one fire. It loads the
// admitted bindings on the first tag it is asked about.
type bindingGate struct {
	admitted         func() ([]VaultBinding, error)
	hookID, pluginID string
	loaded           bool
	bindings         []VaultBinding
	err              error
}

func (g *bindingGate) allow(name string) bool {
	if !g.loaded {
		g.loaded = true
		g.bindings, g.err = g.admitted()
	}
	if g.err != nil || g.pluginID == "" {
		return false
	}
	return slices.Contains(g.bindings, VaultBinding{HookID: g.hookID, PluginID: g.pluginID, VaultName: name})
}

// refusal is the seam's error: an admitted source that failed is named as
// the cause, and the guard's own refusal (KindPermissionDenied) is returned
// as is. Any other rehydrator error keeps its kind with a fixed text, since
// a malformed tag's text can quote whatever sits between its brackets.
func (g *bindingGate) refusal(err error) error {
	if g.err != nil {
		return cascade.Wrap(cascade.KindUnavailable, g.err,
			"hooks: the admitted vault bindings are unavailable; the action is not rehydrated")
	}
	kind, ok := cascade.KindOf(err)
	if ok && kind == cascade.KindPermissionDenied {
		return err
	}
	if !ok {
		kind = cascade.KindInternal
	}
	return cascade.New(kind, "hooks: the tagged action parameters could not be rehydrated")
}

// encodeTagged renders params as canonical JSON (sorted keys) without
// HTML escaping, so a tag's angle brackets reach the rehydrator verbatim.
func encodeTagged(params map[string]string) ([]byte, error) {
	if params == nil {
		params = map[string]string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(params); err != nil {
		return nil, cascade.New(cascade.KindInvalidInput, "hooks: action parameters could not be encoded for rehydration")
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// decodePlain reads the rehydrated object back and refuses anything but
// the tagged params' own keys, each exactly once, with every param that
// carried no tag unchanged: a vault value holding JSON syntax must not
// add, repeat or rewrite a param. The error text is fixed, since the
// decoder's own message can quote plaintext.
func decodePlain(raw []byte, tagged map[string]string) (map[string]string, error) {
	bad := cascade.New(cascade.KindIntegrity, "hooks: rehydrated action parameters do not decode to the tagged params")
	var out map[string]string
	if json.Unmarshal(raw, &out) != nil || len(out) != len(tagged) || objectMembers(raw) != len(tagged) {
		return nil, bad
	}
	for k, v := range tagged {
		got, ok := out[k]
		if !ok || (!strings.Contains(v, "<") && got != v) {
			return nil, bad
		}
	}
	return out, nil
}

// objectMembers counts the top-level members of a JSON object of strings,
// so a repeated key (which Unmarshal folds into one) is seen; -1 when raw
// is not such an object.
func objectMembers(raw []byte) int {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return -1
	}
	n := 0
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return -1
		}
		if _, err := dec.Token(); err != nil {
			return -1
		}
		n++
	}
	return n
}
