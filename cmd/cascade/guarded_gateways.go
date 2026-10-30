// Purpose: registers the [gateways] guarded config family with
// internal/runtime. The extractor runs gateway.ParseAccounts (the same
// decoder production code uses) and the comparator is gateway.Compare,
// which reports every added account or changed field as a loosening.
// Inputs: the config tree's Extra map. Outputs: RegisterGuardedFamily
// call at init. Constraints: one decode per family (C5); an invalid
// section is an Extract error, never a default. SPORT: cmd/cascade
// guarded gateways (P1-GWY-02).
package main

import (
	"encoding/json"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func init() {
	runtime.RegisterGuardedFamily(runtime.GuardedFamily{
		Section: "gateways",
		Extract: extractGatewaysFamily,
		Compare: compareGatewaysFamily,
		Apply:   applyGatewaysFamily,
	})
}

// extractGatewaysFamily runs gateway.ParseAccounts over the [gateways]
// section and returns its canonical JSON value. An absent section is
// nil (the shipped default); an invalid section is an error.
func extractGatewaysFamily(extra map[string]interface{}) (json.RawMessage, error) {
	raw, ok := extra["gateways"]
	if !ok || raw == nil {
		return nil, nil
	}
	section, ok := raw.(map[string]interface{})
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "gateways: must be a table")
	}
	accts, err := gateway.ParseAccounts(section)
	if err != nil {
		return nil, err
	}
	if len(accts) == 0 {
		return nil, nil
	}
	return json.Marshal(accts)
}

// applyGatewaysFamily replaces the section with the admitted account table.
func applyGatewaysFamily(extra map[string]interface{}, raw json.RawMessage) {
	delete(extra, "gateways")
	if len(raw) == 0 {
		return
	}
	var accounts []gateway.Account
	if json.Unmarshal(raw, &accounts) != nil {
		return
	}
	section := map[string]interface{}{}
	for _, a := range accounts {
		m := map[string]interface{}{"base_url": a.BaseURL, "header": string(a.Header)}
		if a.Credential.Kind == gateway.CredentialEnv {
			m["key_env"] = a.Credential.EnvVar
		} else {
			m["credential_helper"] = a.Credential.HelperPath
			m["helper_sha256"] = a.Credential.HelperSHA256
		}
		if a.Models != nil {
			m["models"] = a.Models
		}
		if a.Usage.Kind != "" {
			m["usage"] = gatewayUsageTable(a.Usage)
		}
		section[a.Name] = m
	}
	if len(section) > 0 {
		extra["gateways"] = section
	}
}

func gatewayUsageTable(u gateway.UsageSource) map[string]interface{} {
	m := map[string]interface{}{"source": u.Kind, "window": u.Window}
	if u.Kind == "endpoint" {
		m["path"] = u.Path
		m["used_pointer"] = u.UsedPointer
		m["limit_pointer"] = u.LimitPointer
		m["utilization_pointer"] = u.UtilizationPointer
		m["resets_at_pointer"] = u.ResetsAtPointer
	} else {
		if u.UtilizationHeader != "" {
			m["utilization_header"] = u.UtilizationHeader
		}
		if u.ResetsHeader != "" {
			m["resets_header"] = u.ResetsHeader
		}
	}
	return m
}

// compareGatewaysFamily delegates to gateway.Compare, which reports
// every added account or changed field as a loosening path.
func compareGatewaysFamily(base, next json.RawMessage) ([]runtime.LooseningPath, error) {
	var b, n []gateway.Account
	if len(base) > 0 {
		if err := json.Unmarshal(base, &b); err != nil {
			return nil, err
		}
	}
	if len(next) > 0 {
		if err := json.Unmarshal(next, &n); err != nil {
			return nil, err
		}
	}
	return gateway.Compare(b, n), nil
}
