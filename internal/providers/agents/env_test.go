// Purpose: tests for BuildDriverEnv / BuildToolEnv: the exact allowlisted
// keys, proxy variables from the per-spawn proxy alone, the empty NO_PROXY
// kept, every injected secret returned for the scrub, and a real child
// process seeing exactly the six proxy variables.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-15.

package agents

import (
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// proxyPassword is an obviously fake per-spawn proxy credential.
const proxyPassword = "synthetic-proxy-pass-0001"

func testProfile() RuntimeProfile {
	return RuntimeProfile{Home: "/p/home", ConfigDir: "/p/home/config", Tmp: "/p/tmp"}
}

// sixProxy is the per-spawn proxy's Env(): six variables, NO_PROXY empty.
func sixProxy() []string {
	u := "http://cascade:" + proxyPassword + "@127.0.0.1:4455"
	return []string{"HTTPS_PROXY=" + u, "HTTP_PROXY=" + u, "https_proxy=" + u, "http_proxy=" + u, "NO_PROXY=", "no_proxy="}
}

func envHas(env []string, kv string) bool { return slices.Contains(env, kv) }

func envNames(env []string) []string {
	var out []string
	for _, kv := range env {
		n, _, _ := strings.Cut(kv, "=")
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// hostile is a daemon environment full of values that must never be copied.
func hostile(name string) string {
	return map[string]string{
		"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "FAKE_SRC": "synthetic-key-value-0002",
		"HOME": "/home/operator", "NO_PROXY": "*", "no_proxy": "*", "HTTPS_PROXY": "http://x", "https_proxy": "http://x",
		"HTTP_PROXY": "http://x", "http_proxy": "http://x", "ALL_PROXY": "socks5://y", "all_proxy": "socks5://y",
		"LD_PRELOAD": "/evil.so", "FAKE_HOME": "/home/operator/.fake",
	}[name]
}

func fakeAuth() DriverAuth {
	return DriverAuth{Harness: "fake", Class: CredentialAPIKey, KeyEnv: "FAKE_SRC"}
}

func buildFake(t *testing.T, proxy []string) BuiltEnv {
	t.Helper()
	got, err := BuildDriverEnv(fakeBinding("fake"), fakeAuth(), testProfile(), "job-7", "principal-tok-0001", "lease-7", proxy, hostile)
	if err != nil {
		t.Fatalf("BuildDriverEnv: %v", err)
	}
	return got
}

func TestBuildDriverEnvExactAllowlist(t *testing.T) {
	got := buildFake(t, sixProxy())
	want := []string{"CASCADE_JOB_ID", "CASCADE_LEASE_ID", "CASCADE_PRINCIPAL_TOKEN", "FAKE_HOME", "FAKE_KEY", "HOME", "HTTPS_PROXY", "HTTP_PROXY",
		"LANG", "NO_PROXY", "PATH", "TMPDIR", "http_proxy", "https_proxy", "no_proxy"}
	if names := envNames(got.Driver); !slices.Equal(names, want) {
		t.Fatalf("driver env keys = %v, want exactly %v", names, want)
	}
	for _, kv := range []string{"HOME=/p/home", "TMPDIR=/p/tmp", "FAKE_HOME=/p/home/config", "FAKE_KEY=synthetic-key-value-0002",
		"CASCADE_JOB_ID=job-7", "CASCADE_LEASE_ID=lease-7", "CASCADE_PRINCIPAL_TOKEN=principal-tok-0001", "PATH=/usr/bin:/bin", "LANG=C.UTF-8"} {
		if !envHas(got.Driver, kv) {
			t.Fatalf("driver env lacks %q", kv)
		}
	}
	if envHas(got.Driver, "LD_PRELOAD=/evil.so") {
		t.Fatal("a non-allowlisted daemon variable reached the driver")
	}
}

func TestBuildDriverEnvIgnoresInheritedProxyVars(t *testing.T) {
	px := sixProxy()
	got := buildFake(t, px)
	for _, side := range [][]string{got.Driver, got.Tool} {
		for _, kv := range px {
			if !envHas(side, kv) {
				t.Fatalf("env lacks the spawn proxy's %q: %v", kv, side)
			}
		}
		for _, bad := range []string{"NO_PROXY=*", "no_proxy=*", "HTTPS_PROXY=http://x", "https_proxy=http://x", "HTTP_PROXY=http://x", "http_proxy=http://x"} {
			if envHas(side, bad) {
				t.Fatalf("env inherited %q from the daemon", bad)
			}
		}
		for _, n := range envNames(side) {
			if strings.EqualFold(n, "ALL_PROXY") {
				t.Fatalf("env carries %s", n)
			}
		}
	}
	// a proxy env that is incomplete, repeated or carries ALL_PROXY is refused outright.
	for name, bad := range map[string][]string{
		"missing no_proxy": px[:5],
		"duplicate":        append(slices.Clone(px), "NO_PROXY=x"),
		"all_proxy":        append(slices.Clone(px), "ALL_PROXY=socks5://y"),
		"not a pair":       append(slices.Clone(px[:5]), "no_proxy"),
	} {
		_, err := BuildDriverEnv(fakeBinding("fake"), fakeAuth(), testProfile(), "j", "t", "l", bad, hostile)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
}

func TestBuildDriverEnvKeepsEmptyNoProxy(t *testing.T) {
	got := buildFake(t, sixProxy())
	for _, side := range [][]string{got.Driver, got.Tool} {
		if !envHas(side, "NO_PROXY=") || !envHas(side, "no_proxy=") {
			t.Fatalf("an empty NO_PROXY/no_proxy was dropped: %v", side)
		}
	}
	// other empty values are left out, not emitted as KEY=.
	empty := func(string) string { return "" }
	b := fakeBinding("fake")
	b.Credentials = []CredentialSpec{{Class: CredentialSubscription}}
	sub := DriverAuth{Harness: "fake", Class: CredentialSubscription, AuthSource: "/a/auth.json"}
	g, err := BuildDriverEnv(b, sub, testProfile(), "j", "t", "l", sixProxy(), empty)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range envNames(g.Driver) {
		if n == "PATH" || n == "LANG" {
			t.Fatalf("empty %s was emitted", n)
		}
	}
}

func TestBuildDriverEnvReturnsInjectedSecrets(t *testing.T) {
	got := buildFake(t, sixProxy())
	have := map[string]bool{}
	for _, s := range got.Secrets {
		have[string(s)] = true
	}
	for _, want := range []string{"synthetic-key-value-0002", "principal-tok-0001", proxyPassword} {
		if !have[want] {
			t.Fatalf("Secrets lacks %q: got %d values", want, len(got.Secrets))
		}
	}
	// every secret value that sits in the env is also listed: the key, the token and the proxy credential are each in Driver.
	for _, secret := range []string{"synthetic-key-value-0002", "principal-tok-0001", proxyPassword} {
		inEnv := false
		for _, kv := range got.Driver {
			_, v, _ := strings.Cut(kv, "=")
			inEnv = inEnv || strings.Contains(v, secret)
		}
		if !inEnv || !have[secret] {
			t.Fatalf("secret %q: in env %v, in Secrets %v, want both", secret, inEnv, have[secret])
		}
	}
	if len(have) != len(got.Secrets) {
		t.Fatalf("Secrets holds duplicates: %d values, %d distinct", len(got.Secrets), len(have))
	}
	// a subscription spawn has no key, so its secrets are the token and the proxy credential.
	b := fakeBinding("fake")
	sub := DriverAuth{Harness: "fake", Class: CredentialSubscription, AuthSource: "/a/auth.json"}
	g, err := BuildDriverEnv(b, sub, testProfile(), "j", "tok-9", "l", sixProxy(), hostile)
	if err != nil || len(g.Secrets) != 2 || string(g.Secrets[0]) != "tok-9" || string(g.Secrets[1]) != proxyPassword {
		t.Fatalf("subscription secrets = %d, %v, want the token then the proxy credential", len(g.Secrets), err)
	}
}

func TestBuildDriverEnvRefusesBadInputs(t *testing.T) {
	b, a := fakeBinding("fake"), fakeAuth()
	for name, args := range map[string][3]string{"no job": {"", "t", "l"}, "no token": {"j", "", "l"}, "no lease": {"j", "t", ""},
		"newline token": {"j", "t\nX=1", "l"}, "newline job": {"j\n", "t", "l"}} {
		_, err := BuildDriverEnv(b, a, testProfile(), args[0], args[1], args[2], sixProxy(), hostile)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
	// a key value holding a newline would inject a second variable: refused, value never named.
	nl := func(n string) string {
		if n == "FAKE_SRC" {
			return "secret-line-one\nLD_PRELOAD=/evil.so"
		}
		return hostile(n)
	}
	_, err := BuildDriverEnv(b, a, testProfile(), "j", "t", "l", sixProxy(), nl)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "FAKE_KEY") || strings.Contains(err.Error(), "secret-line-one") {
		t.Fatalf("newline key err = %v, want KindInvalidInput naming FAKE_KEY and not the value", err)
	}
	_, err = BuildDriverEnv(b, DriverAuth{Harness: "fake", Class: "nope"}, testProfile(), "j", "t", "l", sixProxy(), hostile)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unoffered class err = %v", err)
	}
}

func TestBuildToolEnvOmitsKeyAndConfigDir(t *testing.T) {
	got := buildFake(t, sixProxy())
	for _, kv := range got.Tool {
		if strings.HasPrefix(kv, "FAKE_KEY=") || strings.HasPrefix(kv, "FAKE_HOME=") {
			t.Fatalf("tool env carries %q", kv)
		}
	}
	if !envHas(got.Driver, "FAKE_KEY=synthetic-key-value-0002") || !envHas(got.Driver, "FAKE_HOME=/p/home/config") {
		t.Fatal("the driver env lost its key or config dir")
	}
	if len(got.Tool) != len(got.Driver)-2 {
		t.Fatalf("tool env has %d entries, want driver %d minus 2", len(got.Tool), len(got.Driver))
	}
	in := []string{"FAKE_KEY=k", "A=1", "FAKE_HOME=h"}
	if out := BuildToolEnv(in, fakeBinding("fake"), "FAKE_KEY"); !slices.Equal(out, []string{"A=1"}) || len(in) != 3 {
		t.Fatalf("BuildToolEnv = %v (input %v)", out, in)
	}
}

// TestDriverChildSeesAllSixProxyVars runs a real child with the built driver
// env and reads back what it sees.
func TestDriverChildSeesAllSixProxyVars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no env(1) on windows")
	}
	bin, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("env(1) unavailable: %v", err)
	}
	px := sixProxy()
	cmd := exec.Command(bin)
	cmd.Env = buildFake(t, px).Driver
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	seen := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		n, v, _ := strings.Cut(line, "=")
		if strings.EqualFold(n, "ALL_PROXY") {
			t.Fatalf("the child sees %s", n)
		}
		if strings.HasSuffix(strings.ToUpper(n), "_PROXY") {
			seen[n] = v
		}
	}
	if len(seen) != 6 {
		t.Fatalf("the child sees %d proxy variables %v, want exactly six", len(seen), seen)
	}
	for _, kv := range px {
		n, v, _ := strings.Cut(kv, "=")
		if got, ok := seen[n]; !ok || got != v {
			t.Fatalf("child %s = %q (set %v), want %q", n, got, ok, v)
		}
	}
	if seen["NO_PROXY"] != "" || seen["no_proxy"] != "" {
		t.Fatalf("the child's NO_PROXY/no_proxy are not empty: %q %q", seen["NO_PROXY"], seen["no_proxy"])
	}
}

// TestBuildDriverEnvRefusesCollidingVariables: a binding whose config-dir or
// key variable is a base name, or the two the same, is refused rather than
// emitted twice with the later value winning.
func TestBuildDriverEnvRefusesCollidingVariables(t *testing.T) {
	for name, edit := range map[string]func(*Binding){
		"config dir is HOME": func(b *Binding) { b.ConfigDirVar = "HOME" },
		"key var is PATH":    func(b *Binding) { b.Credentials[1].KeyVar = "PATH" },
		"key var is proxy":   func(b *Binding) { b.Credentials[1].KeyVar = "https_proxy" },
		"key is config dir":  func(b *Binding) { b.Credentials[1].KeyVar = b.ConfigDirVar },
	} {
		b := fakeBinding("fake")
		edit(&b)
		_, err := BuildDriverEnv(b, fakeAuth(), testProfile(), "j", "tok", "lease", sixProxy(), hostile)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
}
