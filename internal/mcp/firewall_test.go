package mcp

// Purpose: the response firewall's own unit tests. The end-to-end proof
//   that a transport routes through it lives in transport/, driving Serve;
//   these cover the construction and refusal branches that end-to-end test
//   cannot reach.
// SPORT: MCP_RESPONSE_FIREWALL: ADD (tests).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingInterceptor captures what the marshaler handed the firewall.
type recordingInterceptor struct {
	seen  []byte
	tier  egress.SensitivityTier
	class egress.EgressClass
	err   error
}

func (r *recordingInterceptor) Intercept(_ context.Context, token egress.Capability,
	tier egress.SensitivityTier, content []byte) ([]byte, error) {
	r.seen, r.tier, r.class = content, tier, token.Class()
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte("filtered:"), content...), nil
}

func TestResponseMarshalerRoutesThroughTheFirewall(t *testing.T) {
	rec := &recordingInterceptor{}
	m, err := NewResponseMarshaler(rec)
	if err != nil {
		t.Fatalf("NewResponseMarshaler: %v", err)
	}
	out, err := m.Marshal(context.Background(), &Response{JSONRPC: "2.0", Result: "ok"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.HasPrefix(string(out), "filtered:") {
		t.Fatalf("the response did not come back from the firewall: %s", out)
	}
	if !json.Valid(rec.seen) {
		t.Fatalf("the firewall was handed something that is not JSON: %s", rec.seen)
	}
	if rec.class != egress.EgressClassMCP {
		t.Fatalf("capability class = %q, want %q", rec.class, egress.EgressClassMCP)
	}
	if rec.tier != egress.TierInternal {
		t.Fatalf("declared tier = %q, want %q", rec.tier, egress.TierInternal)
	}
}

func TestResponseMarshalerFailsClosed(t *testing.T) {
	if _, err := NewResponseMarshaler(nil); err == nil {
		t.Fatal("a marshaler with no interceptor must be refused")
	}
	var zero *ResponseMarshaler
	if _, err := zero.Marshal(context.Background(), &Response{}); err == nil {
		t.Fatal("an unconfigured marshaler must refuse rather than encode")
	}
	m, err := NewResponseMarshaler(&recordingInterceptor{err: errors.New("substitution unavailable")})
	if err != nil {
		t.Fatalf("NewResponseMarshaler: %v", err)
	}
	out, merr := m.Marshal(context.Background(), &Response{JSONRPC: "2.0"})
	if merr == nil {
		t.Fatal("a substitution failure must refuse")
	}
	if out != nil {
		t.Fatalf("a refusal returned %d bytes to write", len(out))
	}
}

// openProcessVault is the value source this test binary's process
// firewall reads (see TestMain). Tests replace it to plant values or to make
// the vault fail; production has no such seam: the composition root owns
// the one firewall.
var openProcessVault func() (egress.Vault, error)

// TestMain installs this package's process firewall: an engine over
// whatever openProcessVault returns and the process detector, rebuilt per
// call so a test's replacement takes effect. The default vault is an empty
// file vault under a temp dir; nothing here reaches the host keychain.
func TestMain(m *testing.M) {
	os.Exit(runWithProcessFirewall(m))
}

func runWithProcessFirewall(m *testing.M) int {
	dir, err := os.MkdirTemp("", "cascade-mcp-firewall")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp tests: temp dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	vault, err := openVaultFrom(fileVaultConfig(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp tests: process vault:", err)
		return 1
	}
	openProcessVault = func() (egress.Vault, error) { return vault, nil }
	egress.SetProcessEngineFactory(testProcessFactory)
	return m.Run()
}

// testProcessFactory is this binary's process firewall: an engine over
// openProcessVault's current answer and the process detector.
func testProcessFactory(r *egress.Registry) (*egress.Engine, error) {
	v, err := openProcessVault()
	if err != nil {
		return nil, err
	}
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return nil, err
	}
	return egress.NewEngine(r, v, detector)
}

// fileVaultConfig forces the encrypted file vault under dir: a passphrase
// and a runner that always fails, so the platform backend is never used.
func fileVaultConfig(dir string) secrets.Config {
	return secrets.Config{
		Service:        "cascade-mcp-firewall-test",
		Dir:            dir,
		Passphrase:     "firewall-test-pass",
		ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no platform keychain in this test")
		},
	}
}

// openVaultFrom builds an egress vault over whatever custody cfg selects.
func openVaultFrom(cfg secrets.Config) (egress.Vault, error) {
	custody, err := secrets.SelectCustody(cfg)
	if err != nil {
		return nil, err
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return nil, err
	}
	return secrets.NewEgressVault(broker)
}

// tempVault points the process firewall at a file vault under t.TempDir()
// holding each entry in values.
func tempVault(t *testing.T, values map[string]string) {
	t.Helper()
	custody, err := secrets.SelectCustody(fileVaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	for name, value := range values {
		if _, serr := broker.Set(context.Background(), name, []byte(value), secrets.SetUpdate); serr != nil {
			t.Fatalf("seeding %s: %v", name, serr)
		}
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	previous := openProcessVault
	openProcessVault = func() (egress.Vault, error) { return vault, nil }
	t.Cleanup(func() { openProcessVault = previous })
}

// TestMCPResponseRedactsVaultValue: a stored value of at least eight bytes
// with no credential shape leaves the MCP response path only as its vault
// reference, because the marshaler writes through the process firewall.
func TestMCPResponseRedactsVaultValue(t *testing.T) {
	const name, value = "MCP_CANARY", "plain-canary-9f"
	tempVault(t, map[string]string{name: value})
	m, err := NewDefaultResponseMarshaler()
	if err != nil {
		t.Fatalf("NewDefaultResponseMarshaler: %v", err)
	}
	out, err := m.Marshal(context.Background(), &Response{
		JSONRPC: "2.0", Result: map[string]string{"output": "value " + value},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), value) {
		t.Fatalf("the stored value crossed the MCP response verbatim: %s", out)
	}
	if !strings.Contains(string(out), name) {
		t.Fatalf("the stored value was not replaced by its vault reference: %s", out)
	}
}

// TestDefaultResponseMarshalerRefusesWithoutAProcessFirewall: no firewall
// installed, no marshaler.
func TestDefaultResponseMarshalerRefusesWithoutAProcessFirewall(t *testing.T) {
	egress.SetProcessEngineFactory(nil)
	t.Cleanup(func() {
		egress.SetProcessEngineFactory(testProcessFactory)
	})
	m, err := NewDefaultResponseMarshaler()
	if err != egress.ErrNoProcessFirewall || m != nil { //nolint:errorlint // identity: the sentinel itself
		t.Fatalf("NewDefaultResponseMarshaler = (%v, %v), want (nil, egress.ErrNoProcessFirewall)", m, err)
	}
}

func TestDefaultResponseMarshalerIsUsable(t *testing.T) {
	tempVault(t, nil)
	// Split so no contiguous credential-shaped literal exists in the
	// source; the runtime value, and therefore the assertion, is
	// unchanged.
	shaped := "AKIA" + "7YQ2XPLM4RZV6WTB"
	m, err := NewDefaultResponseMarshaler()
	if err != nil {
		t.Fatalf("NewDefaultResponseMarshaler: %v", err)
	}
	out, merr := m.Marshal(context.Background(), &Response{
		JSONRPC: "2.0", Result: map[string]string{"output": "key " + shaped + " here"},
	})
	if merr != nil {
		t.Fatalf("Marshal: %v", merr)
	}
	if strings.Contains(string(out), shaped) {
		t.Fatalf("the default marshaler let a shaped credential through: %s", out)
	}
}

// TestDefaultMarshalerSubstitutesAShapelessVaultValue is the proof that
// the exact-value half of the substitution pass is live. The stored value
// has no credential shape whatsoever, so the detector half cannot see it;
// only a bound vault can. Before the vault was bound this response
// crossed unredacted.
func TestDefaultMarshalerSubstitutesAShapelessVaultValue(t *testing.T) {
	const shapeless = "correct-horse-battery-staple"
	tempVault(t, map[string]string{"TEAM_PASSPHRASE": shapeless})
	m, err := NewDefaultResponseMarshaler()
	if err != nil {
		t.Fatalf("NewDefaultResponseMarshaler: %v", err)
	}
	out, merr := m.Marshal(context.Background(), &Response{
		JSONRPC: "2.0", Result: map[string]string{"output": "the passphrase is " + shapeless},
	})
	if merr != nil {
		t.Fatalf("Marshal: %v", merr)
	}
	if strings.Contains(string(out), shapeless) {
		t.Fatalf("a vault-held value with no credential shape crossed the firewall: %s", out)
	}
}

// TestDefaultResponseMarshalerFailsClosedWithoutAVault pins the refusal:
// a process that cannot open its vault gets no marshaler, so the
// transport writes nothing. An empty-value-source fallback would look
// identical to a working firewall.
func TestDefaultResponseMarshalerFailsClosedWithoutAVault(t *testing.T) {
	previous := openProcessVault
	openProcessVault = func() (egress.Vault, error) {
		return nil, cascade.New(cascade.KindUnavailable, "mcp: test vault refusal")
	}
	t.Cleanup(func() { openProcessVault = previous })
	if _, err := NewDefaultResponseMarshaler(); err == nil {
		t.Fatal("a marshaler was built over a vault this process could not open")
	}
}
