package build

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// httpListenerFixture writes each named fixture from
// testdata/seeded-violations/http-listener to its slash path under a fresh
// module root and returns the root.
func httpListenerFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for dest, name := range files {
		body, err := os.ReadFile(filepath.Join("testdata", "seeded-violations", "http-listener", name+".go.txt"))
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestHTTPListenerGate_RealTreeGreen(t *testing.T) {
	got, err := UnlistedHTTPListeners(archModuleRoot(t))
	if err != nil {
		t.Fatalf("http listener gate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("listener or mux outside the allowed composition points (%d):\n%s", len(got), strings.Join(got, "\n"))
	}
}

func TestHTTPListenerGate_SeededRed(t *testing.T) {
	cases := []struct{ name, dest, want string }{
		{"servemux", "internal/fake/sse.go", "internal/fake/sse.go:7: net/http.NewServeMux"},
		{"listenconfig", "internal/fake/lc.go", "internal/fake/lc.go:10: (*net.ListenConfig).Listen"},
		{"listenandserve", "cmd/fake/main.go", "cmd/fake/main.go:6: net/http.ListenAndServe"},
		{"listentcp", "plugins/fake/tcp.go", "plugins/fake/tcp.go:7: net.ListenTCP"},
		{"alias", "internal/fake/alias.go", "internal/fake/alias.go:6: net/http.Serve"},
		{"embedded", "internal/fake/embed.go", "internal/fake/embed.go:12: (*net/http.Server).Serve"},
		{"serverlit", "internal/fake/lit.go", "internal/fake/lit.go:7: value of net/http.Server"},
		{"newmux", "internal/fake/new.go", "internal/fake/new.go:7: value of net/http.ServeMux"},
		{"zerovalue", "internal/fake/zero.go", "internal/fake/zero.go:7: value of net/http.ServeMux"},
		{"tagged", "internal/fake/pam.go", "internal/fake/pam.go:9: net.Listen"},
		{"windows", "internal/fake/win.go", "internal/fake/win.go:9: net.Listen"},
		{"filelistener", "internal/fake/fd.go", "internal/fake/fd.go:10: net.FileListener"},
		{"wraplistener", "internal/fake/wrap.go", "internal/fake/wrap.go:7: value of net.Listener"},
		{"defaultmux", "internal/fake/dmux.go", "internal/fake/dmux.go:8: net/http.Handle"},
		// A server reached through an interface is still a reported value.
		{"rehandle", "internal/fake/rehandle.go", "internal/fake/rehandle.go:11: value of net/http.Server"},
		// A near miss of an allowed row is still reported: rows are exact.
		{"servemux", "internal/daemon/daemon2.go", "internal/daemon/daemon2.go:7: net/http.NewServeMux"},
		{"servemux", "cmd/cascade/node_serve_extra.go", "cmd/cascade/node_serve_extra.go:7: net/http.NewServeMux"},
		{"servemux", "internal/providers/agents/egressproxyx/sse.go", "internal/providers/agents/egressproxyx/sse.go:7: net/http.NewServeMux"},
	}
	for _, c := range cases {
		t.Run(c.name+"@"+c.dest, func(t *testing.T) {
			got, err := UnlistedHTTPListeners(httpListenerFixture(t, map[string]string{c.dest: c.name}))
			if err != nil {
				t.Fatalf("gate error: %v", err)
			}
			if !slices.Contains(got, c.want) {
				t.Fatalf("violations = %q, want %q among them", got, c.want)
			}
			for _, v := range got {
				if !strings.HasPrefix(v, c.dest+":") {
					t.Fatalf("violation %q is outside the seeded file %s", v, c.dest)
				}
			}
		})
	}
}

func TestHTTPListenerGate_AllowedAndDelegated(t *testing.T) {
	root := httpListenerFixture(t, map[string]string{
		"internal/daemon/daemon.go":                    "servemux",
		"cmd/cascade/node_serve.go":                    "servemux",
		"internal/providers/agents/egressproxy/x/p.go": "listentcp",
		"providers/agents/driver/drv.go":               "agents",
	})
	got, err := UnlistedHTTPListeners(root)
	if err != nil || len(got) != 0 {
		t.Fatalf("allowed or delegated fixtures reported: %q, error %v", got, err)
	}
	// The same tree plus one unlisted file is red, so the green above is not
	// a gate that reports nothing.
	extra := httpListenerFixture(t, map[string]string{"internal/fake/sse.go": "servemux"})
	body, err := os.ReadFile(filepath.Join(extra, "internal", "fake", "sse.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "fake", "sse.go"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = UnlistedHTTPListeners(root)
	if err != nil || len(got) == 0 {
		t.Fatalf("unlisted file beside allowed ones: %q, error %v", got, err)
	}
	for _, v := range got {
		if !strings.HasPrefix(v, "internal/fake/sse.go:") {
			t.Fatalf("finding %q is outside the one unlisted file", v)
		}
	}
}

func TestHTTPListenerGate_FailClosed(t *testing.T) {
	for _, name := range []string{"parseerror", "typeerror", "cgoonly"} {
		t.Run(name, func(t *testing.T) {
			root := httpListenerFixture(t, map[string]string{
				"internal/fake/bad.go":  name,
				"internal/fake2/sse.go": "servemux",
			})
			got, err := UnlistedHTTPListeners(root)
			if err == nil {
				t.Fatalf("gate passed a tree it cannot type-check: %q", got)
			}
			if got != nil {
				t.Fatalf("gate returned findings alongside an error: %q", got)
			}
		})
	}
	empty := t.TempDir()
	if got, err := UnlistedHTTPListeners(empty); err == nil {
		t.Fatalf("gate passed a root with nothing to scan: %q", got)
	}
}
