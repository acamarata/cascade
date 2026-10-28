// Purpose: registers every guarded config family with internal/runtime. Each
// Extract runs the section's own production decoder (the one its consumers
// call) and returns a value mirroring the TOML table with every shipped
// default omitted; the FieldSpec rules order each key.
// Inputs: the config tree. Outputs: RegisterGuardedFamily calls at init.
// Constraints: one decode per family (C5); an invalid section is an Extract
// error, never a default. [secrets] and [sync] values are built-in
// EffectiveConfig fields, so those families only run the decoder.
// SPORT: cmd/cascade guarded families (P1 config baseline).
package main

import (
	"encoding/json"
	"slices"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/runtime"
	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	ruleAny    = runtime.FieldSpec{Rule: runtime.RuleAnyChange}
	ruleGrows  = runtime.FieldSpec{Rule: runtime.RuleSetGrows}
	ruleSetChg = runtime.FieldSpec{Rule: runtime.RuleSetOrChange}
	ruleTrue   = runtime.FieldSpec{Rule: runtime.RuleRank, Ranks: []interface{}{false, true}}
)

func table(f map[string]runtime.FieldSpec, o *runtime.FieldSpec) runtime.FieldSpec {
	return runtime.FieldSpec{Rule: runtime.RuleTable, Fields: f, Other: o}
}

// The family specs. [policy]: an allow entry, any ask change, a removed deny
// entry, a higher ceiling, a batching change or a removed risk gate loosens; a
// degraded boot (strict profile) keeps an ask overlay at a level strict does not
// deny (askKeep). [conductor.quota.ceiling]: a removed or raised limit loosens.
var (
	askKeep    = []interface{}{policy.L0.String(), policy.L1.String(), policy.L2.String()}
	policySpec = table(map[string]runtime.FieldSpec{
		"allow": ruleGrows, "ask": {Rule: runtime.RuleAnyChange, Keep: askKeep}, "deny": {Rule: runtime.RuleSetShrinks},
		"auto_advance_ceiling":    {Rule: runtime.RuleRank, Ranks: []interface{}{string(policy.CeilingDisabled), string(policy.CeilingTier1), string(policy.CeilingTier2), string(policy.CeilingTier3)}},
		"approval_batch_window_s": ruleAny, "approval_batch_cap": ruleAny,
		"risk_gates": table(nil, &runtime.FieldSpec{Rule: runtime.RuleSetShrinks}),
	}, nil)
	nodesSpec = table(map[string]runtime.FieldSpec{
		"default_trust_tier": ruleAny, "known_hosts_path": ruleAny, "scan_lan": ruleTrue,
		"discovery_networks": ruleGrows, "dispatch_repo_root": ruleAny, "dispatch_remote": ruleAny, "travel": ruleAny,
	}, nil)
	conductorSpec = table(map[string]runtime.FieldSpec{
		"restricted_external_lanes": ruleGrows,
		"quota":                     table(map[string]runtime.FieldSpec{"spill_order": ruleAny, "ceiling": table(nil, &runtime.FieldSpec{Rule: runtime.RuleCap}), "personal_tracking": ruleAny}, nil),
	}, nil)
	egressSpec    = table(nil, &runtime.FieldSpec{Rule: runtime.RuleTable, Fields: map[string]runtime.FieldSpec{"allow": ruleGrows}})
	authSpec      = table(nil, &runtime.FieldSpec{Rule: runtime.RuleTable, Fields: map[string]runtime.FieldSpec{"class": ruleSetChg, "key_env": ruleSetChg, "auth_source": ruleSetChg}})
	lanesSpec     = table(nil, &runtime.FieldSpec{Rule: runtime.RuleTable, Fields: map[string]runtime.FieldSpec{"posture": ruleAny}})
	embeddingSpec = table(map[string]runtime.FieldSpec{"local_socket": ruleSetChg}, nil)
)

// guardedFamilies is the registration table; its order is not significant.
func guardedFamilies() []runtime.GuardedFamily {
	return []runtime.GuardedFamily{
		policySpec.Bind(runtime.GuardedFamily{Section: "policy", Extract: extractPolicyFamily,
			Keys: []string{"policy.allow", "policy.ask", "policy.deny", "policy.auto_advance_ceiling", "policy.risk_gates"}}),
		nodesSpec.Bind(runtime.GuardedFamily{Section: "nodes", Extract: extractNodesFamily,
			Keys: []string{"nodes.default_trust_tier", "nodes.known_hosts_path", "nodes.scan_lan", "nodes.discovery_networks",
				"nodes.dispatch_repo_root", "nodes.dispatch_remote", "nodes.travel"}}),
		conductorSpec.Bind(runtime.GuardedFamily{Section: "conductor", Extract: extractConductorFamily,
			Keys: []string{"conductor.restricted_external_lanes", "conductor.quota"}}),
		egressSpec.Bind(runtime.GuardedFamily{Section: "agents.egress", Extract: extractEgressFamily}),
		authSpec.Bind(runtime.GuardedFamily{Section: "agents.auth", Extract: extractAuthFamily}),
		lanesSpec.Bind(runtime.GuardedFamily{Section: "agents.lanes", Extract: extractLanesFamily}),
		embeddingSpec.Bind(runtime.GuardedFamily{Section: "retrieval.embedding", Extract: extractEmbeddingFamily,
			Keys: []string{"retrieval.embedding.local_socket"}}),
		{Section: "secrets", Extract: extractSecretsFamily, Compare: builtinFamily,
			Keys: []string{"secrets.entropy_floor", "secrets.confidence_threshold"}},
		{Section: "sync", Extract: extractSyncFamily, Compare: builtinFamily},
	}
}

func init() {
	for _, f := range guardedFamilies() {
		runtime.RegisterGuardedFamily(f)
	}
}

// extractPolicyFamily runs policy.ParseConfig (autonomy_profile is built in).
func extractPolicyFamily(extra map[string]interface{}) (json.RawMessage, error) {
	c, err := policy.ParseConfig(extra)
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	for verdict, key := range map[policy.Verdict]string{policy.VerdictAllow: "allow", policy.VerdictAsk: "ask", policy.VerdictDeny: "deny"} {
		names := make([]string, 0, len(c.Overlays[verdict]))
		for _, lvl := range c.Overlays[verdict] {
			names = append(names, lvl.String())
		}
		putList(v, key, names)
	}
	if c.AutoAdvanceCeiling != policy.CeilingDisabled {
		v["auto_advance_ceiling"] = string(c.AutoAdvanceCeiling)
	}
	if c.Batching.WindowSeconds != policy.DefaultApprovalBatchWindowSeconds {
		v["approval_batch_window_s"] = c.Batching.WindowSeconds
	}
	if c.Batching.Cap != policy.DefaultApprovalBatchCap {
		v["approval_batch_cap"] = c.Batching.Cap
	}
	if len(c.RiskGates) > 0 {
		v["risk_gates"] = c.RiskGates
	}
	return json.Marshal(v)
}

// extractNodesFamily runs nodes.LoadSection, the [nodes] decoder.
func extractNodesFamily(extra map[string]interface{}) (json.RawMessage, error) {
	raw, err := subTable(extra, "nodes")
	if err != nil {
		return nil, err
	}
	s, err := nodes.LoadSection(raw)
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	putString(v, "default_trust_tier", s.DefaultTrustTier)
	putString(v, "known_hosts_path", s.KnownHostsPath)
	putString(v, "dispatch_repo_root", s.DispatchRepoRoot)
	putString(v, "dispatch_remote", s.DispatchRemote)
	putList(v, "discovery_networks", s.DiscoveryNetworks)
	for k, on := range map[string]bool{"scan_lan": s.ScanLAN, "travel": s.Travel} {
		if on {
			v[k] = true
		}
	}
	return json.Marshal(v)
}

// extractConductorFamily runs ParseRestrictedPermits and ParseQuotaConfig; a
// present [conductor.quota] is kept even when empty (it ends the divergence event).
func extractConductorFamily(extra map[string]interface{}) (json.RawMessage, error) {
	section, err := subTable(extra, "conductor")
	if err != nil {
		return nil, err
	}
	if _, err := conductor.ParseRestrictedPermits(section); err != nil {
		return nil, err
	}
	q, missing, err := conductor.ParseQuotaConfig(extra)
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	var lanes []string // a list of lane names; ParseRestrictedPermits checked it
	raw, _ := json.Marshal(section["restricted_external_lanes"])
	_ = json.Unmarshal(raw, &lanes)
	putList(v, "restricted_external_lanes", lanes)
	if !missing {
		quota := map[string]interface{}{}
		if len(q.SpillOrder) > 0 {
			quota["spill_order"] = q.SpillOrder
		}
		if len(q.CeilingOverrides) > 0 {
			quota["ceiling"] = q.CeilingOverrides
		}
		v["quota"] = quota
	}
	return json.Marshal(v)
}

// extractEgressFamily runs ParseEgressAllowlists; empty (deny all) is the default.
func extractEgressFamily(extra map[string]interface{}) (json.RawMessage, error) {
	lists, err := egressproxy.ParseEgressAllowlists(extra)
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	for id, list := range lists {
		dests, entry := make([]string, len(list)), map[string]interface{}{}
		for i, d := range list {
			dests[i] = string(d)
		}
		if putList(entry, "allow", dests); len(entry) > 0 {
			v[string(id)] = entry
		}
	}
	return json.Marshal(v)
}

// extractAuthFamily runs agents.ParseDriverAuth against the registered bindings.
func extractAuthFamily(extra map[string]interface{}) (json.RawMessage, error) {
	auths, err := agents.ParseDriverAuth(extra, agents.Bindings())
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	for h, a := range auths {
		entry := map[string]interface{}{}
		putString(entry, "class", a.Class)
		putString(entry, "key_env", a.KeyEnv)
		putString(entry, "auth_source", a.AuthSource)
		v[string(h)] = entry
	}
	return json.Marshal(v)
}

// extractLanesFamily models [agents.lanes.<lane>].posture ahead of its reader.
func extractLanesFamily(extra map[string]interface{}) (json.RawMessage, error) {
	lanes, err := subTable(extra, "agents", "lanes")
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	for lane := range lanes {
		t, err := subTable(lanes, lane)
		if err != nil {
			return nil, err
		}
		entry := map[string]interface{}{}
		if err := putStringKey(entry, t, "posture", "agents.lanes."+lane); err != nil {
			return nil, err
		}
		if len(entry) > 0 {
			v[lane] = entry
		}
	}
	return json.Marshal(v)
}

// extractEmbeddingFamily models retrieval.embedding.local_socket (read by the local embedding lane).
func extractEmbeddingFamily(extra map[string]interface{}) (json.RawMessage, error) {
	embedding, err := subTable(extra, "retrieval", "embedding")
	if err != nil {
		return nil, err
	}
	v := map[string]interface{}{}
	if err := putStringKey(v, embedding, "local_socket", "retrieval.embedding"); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// extractSecretsFamily and extractSyncFamily run their consumers' decoders and
// return nothing: the values are built-in EffectiveConfig fields, so
// builtinFamily never has two values to compare (invalid is handled first).
func builtinFamily(_, _ json.RawMessage) ([]runtime.LooseningPath, error) { return nil, nil }

func extractSecretsFamily(extra map[string]interface{}) (json.RawMessage, error) {
	_, err := detectionConfigFrom(extra)
	return nil, err
}

func extractSyncFamily(extra map[string]interface{}) (json.RawMessage, error) {
	_, err := syncpkg.ParseSection(extra, nil)
	return nil, err
}

// subTable returns the table at m[path[0]][path[1]]...: nil when absent, an
// error when a step is present and not a table.
func subTable(m map[string]interface{}, path ...string) (map[string]interface{}, error) {
	for _, key := range path {
		raw, ok := m[key]
		if !ok || raw == nil {
			return nil, nil
		}
		if m, ok = raw.(map[string]interface{}); !ok {
			return nil, cascade.Newf(cascade.KindInvalidInput, "[%s] must be a table", key)
		}
	}
	return m, nil
}

func putString(v map[string]interface{}, key, s string) {
	if s != "" {
		v[key] = s
	}
}

// putStringKey copies t[key] into v when it is a non-empty string and refuses
// a value of any other type.
func putStringKey(v, t map[string]interface{}, key, table string) error {
	raw, ok := t[key]
	s, isStr := raw.(string)
	if ok && !isStr {
		return cascade.Newf(cascade.KindInvalidInput, "%s.%s must be a string", table, key)
	}
	putString(v, key, s)
	return nil
}

func putList(v map[string]interface{}, key string, list []string) {
	if len(list) > 0 {
		v[key] = slices.Compact(slices.Sorted(slices.Values(list)))
	}
}
