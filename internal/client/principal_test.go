//go:build integration

package client

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
)

type principalHeaderRecorder struct{ headers http.Header }

func (r *principalHeaderRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.headers = req.Header.Clone()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}

func TestPrincipalHeaders(t *testing.T) {
	recorder := &principalHeaderRecorder{}
	transport := &principalTransport{base: recorder}
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "GET"} {
		req, err := http.NewRequestWithContext(context.Background(), method, "http://unix/rpc", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if recorder.headers.Get(principal.HeaderOwnerCLI) != "owner-cli" || recorder.headers.Get(principal.HeaderToken) != "" {
			t.Fatal(recorder.headers)
		}
	}
	for _, token := range []string{"cpt1_" + strings.Repeat("x", 43), ""} {
		t.Setenv(principal.EnvToken, token)
		req, err := http.NewRequestWithContext(context.Background(), "POST", "http://unix/rpc", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(principal.HeaderOwnerCLI, "owner-cli")
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if recorder.headers.Get(principal.HeaderOwnerCLI) != "" || recorder.headers.Get(principal.HeaderToken) != token {
			t.Fatal("ambiguous identity")
		}
		if _, present := recorder.headers[http.CanonicalHeaderKey(principal.HeaderToken)]; !present {
			t.Fatal("empty environment downgraded to owner")
		}
	}
}

// daemonTransportExempt names production transports that reach a daemon
// socket from a separate plugin process. Their identity is a launcher
// binding (the launcher mints and passes the token), not this SDK's rule.
var daemonTransportExempt = map[string]string{
	"plugins/pbd/dispatch_cmd.go": "plugin process; the launcher binds its principal token",
}

// TestNoBareDaemonTransport is the grep gate: outside this package, every
// production http.Transport literal is wrapped by NewPrincipalTransport, so
// no daemon client can reach the socket without the caller identity.
func TestNoBareDaemonTransport(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	scanned, wrapped, bare, err := scanDaemonTransports(root)
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 || wrapped < 2 {
		t.Fatalf("gate scanned %d files and saw %d wrapped transports; the walk missed the tree", scanned, wrapped)
	}
	for rel := range daemonTransportExempt {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("stale daemon transport exemption %s: %v", rel, err)
		}
	}
	for _, b := range bare {
		t.Errorf("bare http.Transport outside internal/client: %s", b)
	}
}

// scanDaemonTransports walks production Go files outside this package and
// sorts every http.Transport literal into wrapped or bare.
func scanDaemonTransports(root string) (scanned, wrapped int, bare []string, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || rel == "internal/client") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || daemonTransportExempt[rel] != "" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "http.Transport{") {
				continue
			}
			if strings.Contains(line, "NewPrincipalTransport(") {
				wrapped++
				continue
			}
			bare = append(bare, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
		}
		return nil
	})
	return scanned, wrapped, bare, err
}
