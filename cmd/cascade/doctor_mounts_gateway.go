// Purpose: disclose the posture of registered gateway providers.
// Inputs: injected provider paths and clock.
// Outputs: a read-only doctor check over providers.db.
// Constraints: no credential resolution or network calls.
// SPORT: cli.doctor.gateway/ADD.
package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
)

type gatewayPostureCheck struct{ paths runtime.PathProvider }

// Name identifies the gateway posture check.
func (gatewayPostureCheck) Name() string { return "gateway_posture" }

// Describe states what the check measures.
func (gatewayPostureCheck) Describe() string {
	return "Report operator-configured gateway terms posture"
}

// Metadata marks the check as diagnostic only.
func (gatewayPostureCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

// Fix refuses changes because gateway posture requires operator judgment.
func (gatewayPostureCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run reports every gateway provider without consulting a credential source.
func (c gatewayPostureCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	result := doctor.CheckResult{Status: doctor.StatusOK, Message: "no gateway providers registered"}
	if _, err := os.Stat(filepath.Join(c.paths.DataDir(), providerRegistryDBFile)); os.IsNotExist(err) {
		return result, nil
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(c.paths.DataDir(), providerRegistryDBFile)), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return result, err
	}
	defer func() { _ = db.Close() }()
	recs, err := registry.NewRegistry(db, runtime.NewSystemClock()).ListProviders(ctx)
	if err != nil {
		return result, err
	}
	var names []string
	for _, rec := range recs {
		if rec.Auth == registry.AuthGateway {
			names = append(names, rec.Name)
		}
	}
	if len(names) > 0 {
		result.Message = "gateway providers " + strings.Join(names, ", ") + ": terms-sensitive; operator-configured"
	}
	return result, nil
}
