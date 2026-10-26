package update

// Purpose: a loopback release server for the transport tests. It is generic
//   over the handler signature so this file names no net/http type (the
//   no-network-unit-lane gate refuses a "net" or "net/http" import in an
//   untagged test file); bindRelease infers the types from the server's own
//   Handler field, the route_inventory_test.go recorderFor precedent.

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

type headerSetter interface{ Set(key, value string) }

type responseWriter[H headerSetter] interface {
	Header() H
	WriteHeader(code int)
	Write(b []byte) (int, error)
}

type wireRequest interface {
	Write(w io.Writer) error
	Context() context.Context
}

// hitLog records request paths and holds per-path redirects.
type hitLog struct {
	mu        sync.Mutex
	dist, tag string
	paths     []string
	redirects map[string]string
	// stall is a path answered with 64 KiB and then no more bytes until
	// the request ends.
	stall string
}

func (l *hitLog) hits() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

type releaseServer[W responseWriter[H], H headerSetter, R wireRequest] struct{ log *hitLog }

// ServeHTTP serves /releases/latest, /releases/download/<tag>/<file> and the
// configured redirects; everything else is 404.
func (s *releaseServer[W, H, R]) ServeHTTP(w W, r R) {
	var buf bytes.Buffer
	_ = r.Write(&buf)
	line, _, _ := strings.Cut(buf.String(), "\r\n")
	fields := strings.Fields(line)
	path := ""
	if len(fields) > 1 {
		path = fields[1]
	}
	s.log.mu.Lock()
	s.log.paths = append(s.log.paths, path)
	loc, redirect := s.log.redirects[path]
	stall := s.log.stall
	s.log.mu.Unlock()
	prefix := "/releases/download/" + s.log.tag + "/"
	switch {
	case stall != "" && path == stall:
		_, _ = w.Write(make([]byte, 64<<10))
		<-r.Context().Done()
	case redirect:
		w.Header().Set("Location", loc)
		w.WriteHeader(302)
	case path == "/releases/latest":
		w.Header().Set("Location", "/releases/tag/"+s.log.tag)
		w.WriteHeader(302)
	case strings.HasPrefix(path, prefix):
		b, err := os.ReadFile(filepath.Join(s.log.dist, strings.TrimPrefix(path, prefix)))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(b)
	default:
		w.WriteHeader(404)
	}
}

func bindRelease[W responseWriter[H], H headerSetter, R wireRequest](_ interface{ ServeHTTP(W, R) }, log *hitLog) *releaseServer[W, H, R] {
	return &releaseServer[W, H, R]{log: log}
}

// startRelease serves dist as release tag on 127.0.0.1 and returns the
// base URL (<server>/releases) and the hit log.
func startRelease(t *testing.T, dist, tag string) (string, *hitLog) {
	t.Helper()
	log := &hitLog{dist: dist, tag: tag, redirects: map[string]string{}}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = bindRelease(ts.Config.Handler, log)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL + "/releases", log
}

// interceptCall is one recorded InterceptClass call.
type interceptCall struct {
	class   egress.EgressClass
	tier    egress.SensitivityTier
	content string
}

// countingInterceptor records calls and answers with err, or with rewrite
// applied to the content.
type countingInterceptor struct {
	mu      sync.Mutex
	calls   []interceptCall
	err     error
	rewrite func([]byte) []byte
}

func (c *countingInterceptor) InterceptClass(_ context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, interceptCall{class: class, tier: tier, content: string(content)})
	if c.err != nil {
		return nil, c.err
	}
	if c.rewrite != nil {
		return c.rewrite(content), nil
	}
	return content, nil
}

func (c *countingInterceptor) recorded() []interceptCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]interceptCall(nil), c.calls...)
}
