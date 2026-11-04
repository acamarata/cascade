//go:build !windows

// Purpose: the gateway-quota daemon wiring (P1-GWY-04): the capacity
// compositors read quota windows from the provider usage store, and one
// supervised poller per admitted gateway account with an endpoint usage
// source records them every 60 s. Every poll acquires the provider-usage
// egress capability first; without it no request is made.
// SPORT: cmd/cascade daemon registrations (P1-GWY-04).
package main

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "gateway-quota", Phase: phaseFleet, Order: 20,
	Wire: wireGatewayQuota,
})

// quotaDoer is the poller's HTTP seam: one GET of url carrying the one
// auth header, returning the status and at most quotaBodyLimit body bytes.
type quotaDoer func(ctx context.Context, url, header, value string) (int, []byte, error)

// httpQuotaDoer is the production quotaDoer over client.
func httpQuotaDoer(client *http.Client) quotaDoer {
	return func(ctx context.Context, url, header, value string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set(header, value)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, quotaBodyLimit))
		return resp.StatusCode, body, err
	}
}

// quotaPoll is everything one poll needs; tests replace each part.
type quotaPoll struct {
	Egress    *egress.Registry
	Doer      quotaDoer
	Token     func(context.Context, gateway.Account) ([]byte, error)
	Store     *usage.Manager
	Clock     runtime.Clock
	NewTicker func(time.Duration) (<-chan time.Time, func())
}

// quotaBodyLimit bounds a usage response body.
const quotaBodyLimit = 64 << 10

// newGatewayQuotaPoll builds the production poll parts; tests replace it.
var newGatewayQuotaPoll = func(store *usage.Manager, clock runtime.Clock, src gateway.Source) quotaPoll {
	return quotaPoll{Egress: egress.DefaultRegistry(), Doer: httpQuotaDoer(&http.Client{Timeout: 20 * time.Second}),
		Token: src.Token, Store: store, Clock: clock, NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}}
}

// wireGatewayQuota opens provider-usage.db, installs its reader as the quota
// source and starts the endpoint pollers.
func wireGatewayQuota(w *daemonWiring) error {
	if w.Paths == nil {
		return nil
	}
	db, err := openMigratedDB(w.Ctx, filepath.Join(w.Paths.DataDir(), providerUsageDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, w.Clock, "", "")
		})
	if err != nil {
		return err
	}
	store := usage.NewManager(db, w.Clock)
	// The token source reads the start snapshot (P1-GWY-10).
	accounts, src, err := daemonGateways(w.Deps.Config, w.Deps.Daemon.Getenv)
	if err != nil {
		_ = db.Close()
		return err
	}
	restore := startGatewayQuota(w.Ctx, fleetCompositor(w.Deps.Fleet), w.Manifest.GoSupervised, accounts,
		newGatewayQuotaPoll(store, w.Clock, src))
	w.Manifest.GoSupervised(w.Ctx, "gateway-quota", "provider usage quota windows", func(ctx context.Context) error {
		<-ctx.Done()
		restore()
		return db.Close()
	})
	return nil
}

// goFunc is daemon.Manifest.GoSupervised's shape.
type goFunc func(ctx context.Context, name, detail string, fn func(context.Context) error)

// startGatewayQuota sets the quota source on comp and as the process
// default, then starts one poller per account with an endpoint source. It
// returns the func that clears the process default.
func startGatewayQuota(ctx context.Context, comp *capacity.Compositor, goSup goFunc, accounts []gateway.Account, p quotaPoll) func() {
	reader := usage.NewReader(p.Store)
	if comp != nil {
		comp.SetQuotaSource(reader.QuotaWindows)
	}
	restore := capacity.SetDefaultQuotaSource(reader.QuotaWindows)
	for _, a := range accounts {
		if a.Usage.Kind == "" || gateway.CheckUsageSource(a.Usage) != nil {
			continue
		}
		acct := a
		goSup(ctx, "gateway-quota/"+acct.Name, "every "+usage.QuotaPollInterval.String(), func(ctx context.Context) error {
			p.run(ctx, acct)
			return nil
		})
	}
	return restore
}

// run polls acct now and on every tick until ctx ends.
func (p quotaPoll) run(ctx context.Context, acct gateway.Account) {
	ticks, stop := p.NewTicker(usage.QuotaPollInterval)
	defer stop()
	for {
		p.pollOnce(ctx, acct)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// pollOnce reads acct's usage endpoint once and records the window, or the
// content-free failure class.
func (p quotaPoll) pollOnce(ctx context.Context, acct gateway.Account) {
	window := topology.BucketWindow(acct.Usage.Window)
	if class := p.fetch(ctx, acct); class != "" {
		_ = p.Store.RecordQuotaFailure(ctx, acct.Name, window, class)
	}
}

// fetch returns "" when the window was recorded, else the failure class.
func (p quotaPoll) fetch(ctx context.Context, acct gateway.Account) string {
	if p.Egress == nil {
		return "egress-refused"
	}
	if _, err := p.Egress.Capability(egress.EgressClassProviderUsage); err != nil {
		return "egress-refused"
	}
	token, err := p.Token(ctx, acct)
	if err != nil {
		return "credential"
	}
	header, value := "Authorization", "Bearer "+string(token)
	if acct.Header == gateway.HeaderXAPIKey {
		header, value = "x-api-key", string(token)
	}
	clear(token)
	status, body, err := p.Doer(ctx, acct.BaseURL+acct.Usage.Path, header, value)
	if err != nil {
		return "unavailable"
	}
	if status != http.StatusOK {
		return "status-" + strconv.Itoa(status)
	}
	obs, err := gateway.DecodeEndpoint(acct.Usage, body, p.Clock.Now())
	if err != nil {
		return "decode"
	}
	obs.Provider = acct.Name
	if err := p.Store.RecordQuota(ctx, obs); err != nil {
		return "store"
	}
	return ""
}
