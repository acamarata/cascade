// Purpose: the hooks.list and hooks.fires handlers over a hooks Dispatcher.
// Inputs: empty params ({} or none); the Dispatcher P1-SUP-02d's
// wire_hooks.go hands registerHooksRPC, and an optional stats source.
// Outputs: hooks.list {hooks, stats} and hooks.fires {fires}.
// Constraints: both verbs are owner-only. No param value, rehydrated value
// or plaintext hash ever reaches the wire: hooks.list carries param keys and
// the hash over the configured params only, and hooks.fires carries the
// dispatcher's scrubbed HookFire records, which hold no params. A nil
// Dispatcher answers KindUnavailable "hooks subsystem degraded".
// SPORT: cmd/cascade hooks RPCs (P1-SUP-02b).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The hooks verbs.
const (
	hooksListMethod  = "hooks.list"
	hooksFiresMethod = "hooks.fires"
)

var (
	_ = rpc.DeclareAccess(hooksListMethod, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(hooksFiresMethod, rpc.AccessOwnerOnly)
)

// errHooksDegraded is what both verbs answer when the hooks subsystem did
// not come up (a nil Dispatcher).
var errHooksDegraded = cascade.New(cascade.KindUnavailable, "hooks subsystem degraded")

// hookEntry is one hooks.list row: identity, the configured params hash
// and the param keys, never a param value.
type hookEntry struct {
	ID         string           `json:"id"`
	Namespace  string           `json:"namespace"`
	Trigger    string           `json:"trigger"`
	ActionType hooks.ActionType `json:"action_type"`
	ParamsHash string           `json:"params_hash"`
	ParamKeys  []string         `json:"param_keys"`
}

// hooksStats is the hooks.list stats object; LastFire is RFC3339 or null.
type hooksStats struct {
	Registered     int     `json:"registered"`
	LastFire       *string `json:"last_fire"`
	BudgetRefusals uint64  `json:"budget_refusals"`
}

// hooksListResult is the hooks.list reply.
type hooksListResult struct {
	Hooks []hookEntry `json:"hooks"`
	Stats hooksStats  `json:"stats"`
}

// hooksFiresResult is the hooks.fires reply.
type hooksFiresResult struct {
	Fires []hooks.HookFire `json:"fires"`
}

// hooksRPC is what the two handlers read.
type hooksRPC struct {
	d     *hooks.Dispatcher
	stats func() hooks.Stats
}

// registerHooksRPC registers hooks.list and hooks.fires over d. stats is
// the counter source for hooks.list; nil reads d.Stats. A nil d still
// registers both verbs, which then answer errHooksDegraded.
func registerHooksRPC(reg *rpc.Registry, d *hooks.Dispatcher, stats func() hooks.Stats) {
	h := hooksRPC{d: d, stats: stats}
	if h.stats == nil && d != nil {
		h.stats = d.Stats
	}
	reg.Register(hooksListMethod, h.list)
	reg.Register(hooksFiresMethod, h.fires)
}

// list answers hooks.list.
func (h hooksRPC) list(_ context.Context, raw json.RawMessage) (any, error) {
	if err := decodeHooksParams(raw); err != nil {
		return nil, err
	}
	if h.d == nil {
		return nil, errHooksDegraded
	}
	configured := h.d.Hooks()
	out := hooksListResult{Hooks: make([]hookEntry, 0, len(configured))}
	for _, hc := range configured {
		out.Hooks = append(out.Hooks, hookEntryOf(hc))
	}
	st := h.stats()
	out.Stats = hooksStats{Registered: st.Registered, BudgetRefusals: st.BudgetRefusals}
	if !st.LastFire.IsZero() {
		last := st.LastFire.UTC().Format(time.RFC3339)
		out.Stats.LastFire = &last
	}
	return out, nil
}

// fires answers hooks.fires: the dispatcher's recent fires, oldest first.
func (h hooksRPC) fires(_ context.Context, raw json.RawMessage) (any, error) {
	if err := decodeHooksParams(raw); err != nil {
		return nil, err
	}
	if h.d == nil {
		return nil, errHooksDegraded
	}
	out := hooksFiresResult{Fires: []hooks.HookFire{}}
	out.Fires = append(out.Fires, h.d.Fires()...)
	return out, nil
}

// hookEntryOf projects hc onto the wire row, dropping every param value.
func hookEntryOf(hc hooks.HookConfig) hookEntry {
	keys := make([]string, 0, len(hc.ActionParams))
	for k := range hc.ActionParams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return hookEntry{
		ID: hc.ID, Namespace: hc.Namespace, Trigger: hc.Trigger, ActionType: hc.ActionType,
		ParamsHash: configParamsHash(hc.ActionParams), ParamKeys: keys,
	}
}

// configParamsHash is the order-independent SHA-256 over the configured
// params (key, NUL, value, NUL in key order), the same construction the
// hooks package uses for HookFire.ParamsHash. It covers the whole map, so
// it never equals the digest of a single value.
func configParamsHash(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(params[k]))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// decodeHooksParams accepts absent, null or {} params and refuses any field.
func decodeHooksParams(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var p struct{}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "hooks: params")
	}
	if _, err := dec.Token(); err != io.EOF {
		return cascade.New(cascade.KindInvalidInput, "hooks: params carry trailing data")
	}
	return nil
}
