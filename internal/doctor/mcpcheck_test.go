package doctor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain intercepts a special env var so this same test binary can act
// as the "local fake stdio server binary" the ticket contract calls for
// (a real subprocess speaking the tools/list protocol over stdio, never
// the network — Art.7.2), without a separate `go build` step. This is
// the standard Go self-exec test-helper-process pattern (as used by
// os/exec's own tests).
func TestMain(m *testing.M) {
	if os.Getenv("CASCADE_DOCTOR_FAKE_MCP_SERVER") == "1" {
		runFakeMCPServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeMCPServer reads one line from stdin (the tools/list request)
// and responds per CASCADE_DOCTOR_FAKE_MCP_MODE: "ok" (default) answers
// a well-formed tools/list result, "rpcerror" answers a JSON-RPC error
// member, "hang" blocks until killed (the unresponsive-server case).
func runFakeMCPServer() {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(1)
	}
	switch os.Getenv("CASCADE_DOCTOR_FAKE_MCP_MODE") {
	case "hang":
		select {}
	case "rpcerror":
		_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"error":{"message":"boom"}}`)
	default:
		_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}
	os.Exit(0)
}

type fakeDiscoverer struct {
	configs []HarnessConfig
	err     error
}

func (f fakeDiscoverer) DiscoverHarnessConfigs(context.Context) ([]HarnessConfig, error) {
	return f.configs, f.err
}

// selfExecPath returns the running test binary's own absolute path, used
// as the fake MCP server's "command" — a real executable resolvable via
// resolveCommandPath.
func selfExecPath(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return p
}

func TestDoctorMcpIntegrationCheck(t *testing.T) {
	self := selfExecPath(t)
	t.Setenv("CASCADE_DOCTOR_FAKE_MCP_SERVER", "1")

	t.Run("no harness configs is ok", func(t *testing.T) {
		c := NewMCPIntegrationCheck(fakeDiscoverer{}, nil)
		res, err := c.Run(context.Background())
		if err != nil || res.Status != StatusOK {
			t.Fatalf("got %+v, err=%v, want StatusOK", res, err)
		}
	})

	t.Run("healthy round trip is ok", func(t *testing.T) {
		t.Setenv("CASCADE_DOCTOR_FAKE_MCP_MODE", "ok")
		c := NewMCPIntegrationCheck(fakeDiscoverer{configs: []HarnessConfig{
			{ConfigPath: "/fake/harness.json", ServerName: "cascade", Command: self},
		}}, nil)
		res, err := c.Run(context.Background())
		if err != nil || res.Status != StatusOK {
			t.Fatalf("got %+v, err=%v, want StatusOK", res, err)
		}
	})

	t.Run("missing registration is error naming config path", func(t *testing.T) {
		c := NewMCPIntegrationCheck(fakeDiscoverer{configs: []HarnessConfig{
			{ConfigPath: "/fake/no-reg.json", ServerName: ""},
		}}, nil)
		res, _ := c.Run(context.Background())
		if res.Status != StatusError || !strings.Contains(res.Detail, "/fake/no-reg.json") || !strings.Contains(res.Detail, "registration missing") {
			t.Fatalf("got %+v, want error naming config path + registration missing", res)
		}
	})

	t.Run("dangling command path is error", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist-binary")
		c := NewMCPIntegrationCheck(fakeDiscoverer{configs: []HarnessConfig{
			{ConfigPath: "/fake/dangling.json", ServerName: "cascade", Command: missing},
		}}, nil)
		res, _ := c.Run(context.Background())
		if res.Status != StatusError || !strings.Contains(res.Detail, "/fake/dangling.json") || !strings.Contains(res.Detail, "does not resolve") {
			t.Fatalf("got %+v, want error naming config path + does not resolve", res)
		}
	})
}

func TestDoctorMcpIntegrationCheck_FailureModes(t *testing.T) {
	self := selfExecPath(t)
	t.Setenv("CASCADE_DOCTOR_FAKE_MCP_SERVER", "1")

	t.Run("unresponsive server is error", func(t *testing.T) {
		t.Setenv("CASCADE_DOCTOR_FAKE_MCP_MODE", "hang")
		c := NewMCPIntegrationCheck(fakeDiscoverer{configs: []HarnessConfig{
			{ConfigPath: "/fake/hang.json", ServerName: "cascade", Command: self},
		}}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		res, _ := c.Run(ctx)
		if res.Status != StatusError || !strings.Contains(res.Detail, "/fake/hang.json") || !strings.Contains(res.Detail, "round-trip failed") {
			t.Fatalf("got %+v, want error naming config path + round-trip failed", res)
		}
	})

	t.Run("rpc error response is error", func(t *testing.T) {
		t.Setenv("CASCADE_DOCTOR_FAKE_MCP_MODE", "rpcerror")
		c := NewMCPIntegrationCheck(fakeDiscoverer{configs: []HarnessConfig{
			{ConfigPath: "/fake/rpcerr.json", ServerName: "cascade", Command: self},
		}}, nil)
		res, _ := c.Run(context.Background())
		if res.Status != StatusError {
			t.Fatalf("got %+v, want StatusError", res)
		}
	})

	t.Run("discovery failure is error", func(t *testing.T) {
		c := NewMCPIntegrationCheck(fakeDiscoverer{err: fmt.Errorf("boom")}, nil)
		res, _ := c.Run(context.Background())
		if res.Status != StatusError {
			t.Fatalf("got %+v, want StatusError", res)
		}
	})

	t.Run("not fixable", func(t *testing.T) {
		c := NewMCPIntegrationCheck(fakeDiscoverer{}, nil)
		if _, err := c.Fix(context.Background()); err != ErrCheckNotFixable {
			t.Fatalf("got err=%v, want ErrCheckNotFixable", err)
		}
	})
}

func TestMCPIntegrationCheck_NameDescribeMetadata(t *testing.T) {
	c := NewMCPIntegrationCheck(fakeDiscoverer{}, nil)
	if c.Name() != "mcp_integration" {
		t.Fatalf("got Name()=%q", c.Name())
	}
	if c.Describe() == "" {
		t.Fatalf("Describe() must not be empty")
	}
	if !c.Metadata().FirstRun || c.Metadata().Fixable {
		t.Fatalf("got Metadata()=%+v, want FirstRun=true Fixable=false", c.Metadata())
	}
}
