package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gateway is the fake's state: the expected token, the control directory,
// the journal and the fault-script counter.
type gateway struct {
	control, token string
	mu             sync.Mutex
	journal        *os.File
	script         string
	served         int
}

func newGateway(control, journal, token string) (*gateway, error) {
	f, err := os.OpenFile(journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-supplied path.
	if err != nil {
		return nil, err
	}
	return &gateway{control: control, token: token, journal: f}, nil
}

func (g *gateway) close() error { return g.journal.Close() }

func (g *gateway) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.authed(g.models))
	mux.HandleFunc("POST /v1/messages", g.authed(g.messages))
	mux.HandleFunc("GET /usage", g.authed(g.usage))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		g.record(r, http.StatusNotFound, g.matched(r))
		writeError(w, http.StatusNotFound, "not_found_error", "not found")
	})
	return mux
}

// matched reports whether r carries the expected token as a bearer or an
// x-api-key header, compared in constant time.
func (g *gateway) matched(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.Header.Get("X-Api-Key")
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(g.token)) == 1
}

// authed refuses a request without the token with the API's 401 shape.
func (g *gateway) authed(h func(http.ResponseWriter, *http.Request) int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.matched(r) {
			g.record(r, http.StatusUnauthorized, false)
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
			return
		}
		g.record(r, h(w, r), true)
	}
}

// record appends one journal line; it never holds headers or bodies.
func (g *gateway) record(r *http.Request, status int, matched bool) {
	line, err := json.Marshal(map[string]any{"method": r.Method, "path": r.URL.Path, "status": status, "bearer_matched": matched})
	if err != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, _ = g.journal.Write(append(line, '\n'))
}

func (g *gateway) models(w http.ResponseWriter, _ *http.Request) int {
	body := `{"data":[` +
		`{"type":"model","id":"model-a","display_name":"Model A","created_at":"2026-01-01T00:00:00Z"},` +
		`{"type":"model","id":"model-b","display_name":"Model B","created_at":"2026-01-01T00:00:00Z"}],` +
		`"has_more":false,"first_id":"model-a","last_id":"model-b"}`
	writeJSON(w, http.StatusOK, body)
	return http.StatusOK
}

// usage serves the constructed usage source: a five-hour window at 37.5 %
// resetting two hours from now, or 503 while the control file says fail.
func (g *gateway) usage(w http.ResponseWriter, _ *http.Request) int {
	if g.controlLine("usage", "ok") == "fail" {
		writeError(w, http.StatusServiceUnavailable, "api_error", "usage source unavailable")
		return http.StatusServiceUnavailable
	}
	resets := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"five_hour":{"used":75,"limit":200,"utilization":37.5,"resets_at":%q}}`, resets))
	return http.StatusOK
}

func (g *gateway) messages(w http.ResponseWriter, r *http.Request) int {
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, &req) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "malformed request body")
		return http.StatusBadRequest
	}
	if shape := g.nextFault(); shape != "" {
		return writeFault(w, shape)
	}
	if req.Stream {
		writeStream(w, req.Model)
		return http.StatusOK
	}
	writeJSON(w, http.StatusOK, messageBody(req.Model))
	return http.StatusOK
}

// nextFault advances the fault script by one message request and returns
// the shape to fail with, or "" to serve. A changed script restarts the count.
func (g *gateway) nextFault() string {
	script := g.controlLine("faults", "ok")
	g.mu.Lock()
	defer g.mu.Unlock()
	if script != g.script {
		g.script, g.served = script, 0
	}
	g.served++
	f := strings.Fields(script)
	switch {
	case len(f) == 2 && f[0] == "always":
		return f[1]
	case len(f) == 5 && f[1] == "x" && f[3] == "then" && f[4] == "ok":
		if n, err := strconv.Atoi(f[0]); err == nil && g.served <= n {
			return f[2]
		}
	}
	return ""
}

// controlLine is the trimmed content of one control file, or def.
func (g *gateway) controlLine(name, def string) string {
	b, err := os.ReadFile(filepath.Join(g.control, name)) //nolint:gosec // test-supplied directory.
	if s := strings.TrimSpace(string(b)); err == nil && s != "" {
		return s
	}
	return def
}
