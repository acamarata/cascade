//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/process"
	hbtest "github.com/acamarata/cascade/internal/plugins/process/testdata/hostcallplugin/harness"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

type boundaryFixture struct {
	h      *plugins.TrustedHandle
	s      *hostServer
	env    plugins.LaunchEnv
	stderr bytes.Buffer
	paths  runtime.PathProvider
}

func boundarySetup(t *testing.T, grants string, transport *hbtest.Transport) *boundaryFixture {
	t.Helper()
	resetProcessFirewall(t)
	dir, data := t.TempDir(), filepath.Join(t.TempDir(), "data")
	goBuild(t, "../../internal/plugins/process/testdata/hostcallplugin", dir, "hostcall")
	t.Cleanup(plugins.SetExecutableDirForTest(dir))
	f := &boundaryFixture{paths: elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: filepath.Dir(data)}, data: data}}
	var store provider.Store
	addTestRegistration(t, daemonRegistration{Name: "host-boundary-test-store", Phase: phaseLate, Order: 9003, Wire: func(w *daemonWiring) error { store = w.Store; return nil }})
	_, call := newElevatedTestRegistry(t, data)
	manifest := "id = \"hostcall\"\nname = \"Host Call\"\nschema = \"cascade.plugin/v2\"\nversion = \"1.0.0\"\nhost_version = \">=2.0.0\"\nruntime = \"process\"\nrequires = [" + grants + "]\n"
	attestedAdd(t, call, "hostcall", manifest)
	clock := runtime.NewSystemClock()
	effects, err := openEffectLog(store, clock, nil, f.paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plugins.RecoverPluginEffects(context.Background(), effects, pluginEffectsDir(f.paths)); err != nil {
		t.Fatal(err)
	}
	f.env = plugins.LaunchEnv{Store: store, Effects: effects, Bus: events.New(store, clock), Clock: clock, Stderr: &f.stderr, Registrar: noopPluginRegistrar{}, Getenv: func(string) string { return "" }}
	t.Cleanup(plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) { return f.env, nil }))
	factory := newHostBoundaryFactory(f.paths, transport)
	t.Cleanup(plugins.SetHostBoundary(func(ctx context.Context, env plugins.LaunchEnv, id string, grants []string) (process.HostCapabilityChecker, error) {
		checker, err := factory(ctx, env, id, grants)
		if err == nil {
			f.s = checker.(*hostServer)
		}
		return checker, err
	}))
	return f
}

func (f *boundaryFixture) launch(t *testing.T) {
	t.Helper()
	var err error
	f.h, err = plugins.LaunchInstalled(context.Background(), "hostcall")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.h.Close(context.Background()) })
}

func (f *boundaryFixture) proxy(t *testing.T, method string, params any, notify bool) (json.RawMessage, *process.RPCError) {
	return hbtest.Proxy(t, f.h, method, params, notify)
}

func TestHostBoundaryGrantsFromRecordGrants(t *testing.T) {
	g := grantsFor("p", []string{"net.http:api.github.com", "net.http:*.example.com", "oauth:repo", "host.secret_ref", "unknown", "net.http:"})
	if strings.Join(g.NetScopes, ",") != "api.github.com,*.example.com" || g.CrossDomain || len(g.StorageDomains) != 1 || !g.StorageDomains["plugin:p"] {
		t.Fatalf("grants=%+v", g)
	}
	if len(grantsFor("p", nil).NetScopes) != 0 {
		t.Fatal("nil grants widened")
	}
}

func TestLaunchedPluginUndeclaredHTTPDeniedAndAudited(t *testing.T) {
	calls := 0
	f := boundarySetup(t, `"host.secret_ref"`, hbtest.NewTransport(func(string) { calls++ }))
	f.launch(t)
	_, err := f.proxy(t, "host_http_request", map[string]string{"url": "https://evil.example"}, false)
	if err == nil || err.Code != cascade.RPCCodeCapabilityDenied || calls != 0 {
		t.Fatalf("denial=%+v calls=%d", err, calls)
	}
	stored, _ := storedAuditRecords(t, f.env.Store)
	if !strings.Contains(stored, "outside the plugin's declared net scope") {
		t.Fatal("missing denial audit")
	}
}

func TestLaunchedPluginDeclaredHTTPGoesThroughEgress(t *testing.T) {
	canary := "shapeless-" + "eight-byte-canary"
	body := ""
	f := boundarySetup(t, `"net.http:api.example.com"`, hbtest.NewTransport(func(b string) { body = b }))
	fw, err := processFirewallFor(f.paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fw.Broker().Set(context.Background(), "host-canary", []byte(canary), secrets.SetUpdate); err != nil {
		t.Fatal(err)
	}
	f.launch(t)
	result, rpcErr := f.proxy(t, "host_http_request", map[string]string{"url": "https://api.example.com", "method": "POST", "body": canary}, false)
	if rpcErr != nil || !strings.Contains(string(result), "response") || strings.Contains(body, canary) || !strings.Contains(body, "HOST_CANARY") {
		t.Fatalf("HTTP result=%s error=%+v; substituted=%v", result, rpcErr, strings.Contains(body, "HOST_CANARY"))
	}
	assertHostEffects(t, f.s.audit, []string{"intent", "confirmed"})
}

func assertHostEffects(t *testing.T, log *audit.Log, want []string) {
	hbtest.Effects(t, log, want)
}

func TestSecretRefNeverReturnsValue(t *testing.T) {
	f := boundarySetup(t, `"host.secret_ref"`, hbtest.NewTransport(nil))
	fw, err := processFirewallFor(f.paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fw.Broker().Set(context.Background(), "plugin.hostcall.credential", []byte("reference-"+"canary-value"), secrets.SetUpdate); err != nil {
		t.Fatal(err)
	}
	f.launch(t)
	result, rpcErr := f.proxy(t, "host_secret_ref", map[string]string{"key": "plugin.hostcall.credential"}, false)
	if rpcErr != nil || !strings.Contains(string(result), "plugin:hostcall:ref:") || strings.Contains(string(result), "canary-value") {
		t.Fatal("reference boundary failed")
	}
	_, rpcErr = f.proxy(t, "host_secret_ref", map[string]string{"key": "plugin:other:credential"}, false)
	if rpcErr == nil || rpcErr.Code != cascade.RPCCodeCapabilityDenied {
		t.Fatal("foreign reference allowed")
	}
}

func TestStorageConfinedToPluginNamespace(t *testing.T) {
	f := boundarySetup(t, `"host.secret_ref"`, hbtest.NewTransport(nil))
	f.launch(t)
	for _, domain := range []string{"plugin:hostcall", "plugin:other", "plugin:hostcall/../other"} {
		_, err := f.proxy(t, "host_storage_put", map[string]string{"domain": domain, "key": "item", "value": "saved"}, false)
		if domain == "plugin:hostcall" && err != nil || domain != "plugin:hostcall" && (err == nil || err.Code != cascade.RPCCodeCapabilityDenied) {
			t.Fatalf("domain=%s error=%+v", domain, err)
		}
	}
	result, err := f.proxy(t, "host_storage_get", map[string]string{"domain": "plugin:hostcall", "key": "item"}, false)
	if err != nil || string(result) != `"saved"` {
		t.Fatal("own storage unavailable")
	}
}

func TestHostBoundaryFactoryConstructionErrorIsUnavailable(t *testing.T) {
	calls := 0
	f := boundarySetup(t, `"net.http:api.example.com"`, hbtest.NewTransport(func(string) { calls++ }))
	t.Cleanup(plugins.SetHostBoundary(newHostBoundaryFactory(nil, hbtest.NewTransport(nil))))
	f.launch(t)
	_, err := f.proxy(t, "host_http_request", map[string]string{"url": "https://api.example.com"}, false)
	if err == nil || err.Code != cascade.RPCCodeUnavailable || calls != 0 {
		t.Fatal("factory failure did not fail closed")
	}
}

func TestNotificationValueNeverPersisted(t *testing.T) {
	f := boundarySetup(t, `"host.secret_ref"`, hbtest.NewTransport(nil))
	f.launch(t)
	canary := "unregistered-" + "notification-canary"
	f.proxy(t, "host_secret_ref", map[string]string{"key": "not-scoped", "value": canary}, true)
	// A subsequent request is a barrier: both notification checks finish first.
	f.proxy(t, "host_log", map[string]string{"msg": "barrier"}, false)
	stored := hbtest.WaitNotification(t, f.env.Store)
	if strings.Contains(stored, canary) || strings.Contains(f.stderr.String(), canary) || !strings.Contains(stored, `"keys":["key","value"]`) {
		t.Fatal("notification audit did not preserve only keys")
	}
}

func TestHostBoundaryUsesEffectiveGrants(t *testing.T) {
	f := boundarySetup(t, `"net.http:api.example.com"`, hbtest.NewTransport(nil))
	rec, _, err := plugins.LoadMetadata(context.Background(), f.env.Store, "hostcall")
	if err != nil {
		t.Fatal(err)
	}
	rec.Grants = append(rec.Grants, "net.http:evil.example")
	if err = plugins.SaveMetadata(context.Background(), f.env.Store, rec); err != nil {
		t.Fatal(err)
	}
	f.launch(t)
	if err = f.s.CheckHTTP(context.Background(), "https://evil.example"); !cascade.HasKind(err, cascade.KindCapabilityDenied) {
		t.Fatal("unwitnessed grant widened")
	}
}

func TestOneHostCallConsumerAfterPluginRestart(t *testing.T) {
	f := boundarySetup(t, `"host.secret_ref"`, hbtest.NewTransport(nil))
	f.launch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = f.h.Call(ctx, "crash", nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := f.h.Call(ctx, "probe", nil)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin did not restart")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.proxy(t, "host_log", map[string]string{"msg": "one"}, false)
	out, err := f.h.Call(context.Background(), "probe", nil)
	if err != nil || string(out) != "1" {
		t.Fatalf("reply count=%s error=%v", out, err)
	}
	// The replaced consumer may still be exiting right after the restart, so
	// poll for exactly one until a short deadline; more than one persisting
	// past it is the leak this test guards against.
	consumers := -1
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		consumers = bytes.Count(allGoroutineStacks(), []byte("process.(*ProcessRuntime).consumeHostCalls("))
		if consumers == 1 || time.Now().After(end) {
			break
		}
	}
	if consumers != 1 {
		t.Fatalf("expected one host call consumer, found %d", consumers)
	}
}

// allGoroutineStacks returns the full dump of every goroutine. A fixed buffer
// truncates the dump when earlier tests leave many goroutines behind, which
// hides the consumer being counted.
func allGoroutineStacks() []byte {
	for size := 1 << 20; ; size *= 2 {
		stack := make([]byte, size)
		if n := goruntime.Stack(stack, true); n < size {
			return stack[:n]
		}
	}
}

func TestHostHTTPEffectCrashBetweenEffectAndConfirm(t *testing.T) {
	root := os.Getenv("CASCADE_HOST_EFFECT_CHILD")
	if root == "" {
		root = t.TempDir()
	}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, root)
	}
	resetProcessFirewall(t)
	if os.Getenv("CASCADE_HOST_EFFECT_CHILD") == "" {
		hbtest.Crash(t, root)
	}
	paths := elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: root}, data: filepath.Join(root, "data")}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(root, "effects.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	effects, err := openEffectLog(store, runtime.NewSystemClock(), nil, paths)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CASCADE_HOST_EFFECT_CHILD") != "" {
		env := plugins.LaunchEnv{Store: store, Effects: effects, Clock: runtime.NewSystemClock()}
		transport := hbtest.NewTransport(func(string) { hbtest.RecordCrash(root) })
		checker, err := newHostBoundaryFactory(paths, transport)(context.Background(), env, "hostcall", []string{"net.http:api.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = checker.(*hostServer).ServeHTTP(context.Background(), process.HostHTTPRequest{URL: "https://api.example.com", Method: "POST"})
		t.Fatal("child survived")
	}
	hbtest.Recover(t, effects, pluginEffectsDir(paths), root)
	log, err := openAuditLog(store, runtime.NewSystemClock(), nil, paths)
	if err != nil {
		t.Fatal(err)
	}
	assertHostEffects(t, log, []string{"intent", "unknown-outcome"})
}
