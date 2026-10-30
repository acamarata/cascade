// Purpose: loosening comparison for the [gateways] guarded family.
// Every added account or changed field is a loosening; removals are
// tightenings and produce no path. The comparator mirrors the keys
// ParseAccounts accepts so an unknown key cannot silently slip through.
// Inputs: two []Account slices (base and next).
// Outputs: a slice of runtime.LooseningPath entries.
// Constraints: pure function, no I/O, no clock.
// SPORT: internal/providers/gateway compare (P1-GWY-02).

package gateway

import (
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/runtime"
)

// Compare reports every loosening between base and next. Adding an
// account, changing any of its fields, or adding a usage block is a
// loosening. Removing an account is a tightening (no path emitted).
func Compare(base, next []Account) []runtime.LooseningPath {
	baseIdx := indexByName(base)
	nextIdx := indexByName(next)
	var out []runtime.LooseningPath
	for name, nb := range nextIdx {
		ob, ok := baseIdx[name]
		if !ok {
			out = append(out, runtime.LooseningPath{Family: "gateways", Key: name})
			continue
		}
		out = append(out, diffAccount(name, ob, nb)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key == out[j].Key {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func indexByName(accts []Account) map[string]Account {
	m := make(map[string]Account, len(accts))
	for _, a := range accts {
		m[a.Name] = a
	}
	return m
}

func diffAccount(name string, a, b Account) []runtime.LooseningPath {
	var out []runtime.LooseningPath
	if a.BaseURL != b.BaseURL {
		out = append(out, lp(name, "base_url", a.BaseURL, b.BaseURL))
	}
	if a.Header != b.Header {
		out = append(out, lp(name, "header", string(a.Header), string(b.Header)))
	}
	out = append(out, diffCredential(name, a.Credential, b.Credential)...)
	if !modelsEqual(a.Models, b.Models) {
		out = append(out, lp(name, "models", modelsStr(a.Models), modelsStr(b.Models)))
	}
	out = append(out, diffUsage(name, a.Usage, b.Usage)...)
	return out
}

func diffCredential(name string, a, b Credential) []runtime.LooseningPath {
	var out []runtime.LooseningPath
	if a.Kind != b.Kind {
		out = append(out, lp(name, "credential_kind", string(a.Kind), string(b.Kind)))
		return out
	}
	switch a.Kind {
	case CredentialEnv:
		if a.EnvVar != b.EnvVar {
			out = append(out, lp(name, "key_env", a.EnvVar, b.EnvVar))
		}
	case CredentialHelper:
		if a.HelperPath != b.HelperPath {
			out = append(out, lp(name, "credential_helper", a.HelperPath, b.HelperPath))
		}
		if a.HelperSHA256 != b.HelperSHA256 {
			out = append(out, lp(name, "helper_sha256", a.HelperSHA256, b.HelperSHA256))
		}
	}
	return out
}

func diffUsage(name string, a, b UsageSource) []runtime.LooseningPath {
	var out []runtime.LooseningPath
	if a.Kind != b.Kind {
		out = append(out, lp(name, "usage.source", a.Kind, b.Kind))
	}
	if a.Window != b.Window {
		out = append(out, lp(name, "usage.window", a.Window, b.Window))
	}
	if a.Path != b.Path {
		out = append(out, lp(name, "usage.path", a.Path, b.Path))
	}
	if a.UsedPointer != b.UsedPointer {
		out = append(out, lp(name, "usage.used_pointer", a.UsedPointer, b.UsedPointer))
	}
	if a.LimitPointer != b.LimitPointer {
		out = append(out, lp(name, "usage.limit_pointer", a.LimitPointer, b.LimitPointer))
	}
	if a.UtilizationPointer != b.UtilizationPointer {
		out = append(out, lp(name, "usage.utilization_pointer", a.UtilizationPointer, b.UtilizationPointer))
	}
	if a.ResetsAtPointer != b.ResetsAtPointer {
		out = append(out, lp(name, "usage.resets_at_pointer", a.ResetsAtPointer, b.ResetsAtPointer))
	}
	if a.UtilizationHeader != b.UtilizationHeader {
		out = append(out, lp(name, "usage.utilization_header", a.UtilizationHeader, b.UtilizationHeader))
	}
	if a.ResetsHeader != b.ResetsHeader {
		out = append(out, lp(name, "usage.resets_header", a.ResetsHeader, b.ResetsHeader))
	}
	return out
}

func lp(name, field, cur, prop string) runtime.LooseningPath {
	return runtime.LooseningPath{
		Family:   "gateways",
		Key:      name + "." + field,
		Current:  cur,
		Proposed: prop,
	}
}

func modelsEqual(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func modelsStr(m []string) string {
	b, _ := json.Marshal(m)
	return string(b)
}
