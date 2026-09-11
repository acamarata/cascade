//go:build integration

package registryfetch_test

// Purpose: the real-socket counterpart to fetch_test.go's fakeDoer
//   suite: proves realDoer (doer.go) actually performs an HTTP GET over
//   a real loopback socket. Carries the integration build tag per
//   AGENT-BRIEF.md ("real-socket tests go behind the integration build
//   tag"), so it is excluded from the default unit lane and from the
//   ticket's own coverage-floor check (neither runs with -tags
//   integration).
// SPORT: internal/plugins/registryfetch (ADD) — P1-E24-W5-S50-T1.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/acamarata/cascade/internal/plugins/registryfetch"
)

func TestHTTPFetcher_RealDoer_Integration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"schema_version":"1","entries":[]}`))
	}))
	defer srv.Close()

	f := registryfetch.HTTPFetcher{BaseURL: srv.URL + "/"}
	data, err := f.FetchIndex(context.Background())
	if err != nil {
		t.Fatalf("FetchIndex over real socket: %v", err)
	}
	if string(data) != `{"schema_version":"1","entries":[]}` {
		t.Fatalf("FetchIndex over real socket = %q", data)
	}
}
