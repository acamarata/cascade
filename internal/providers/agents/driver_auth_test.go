// Purpose: tests for the [agents.auth.<harness>] parser and the closed
// credential table lookup: config picks a class and a source, never a
// variable name, and the key value is read from the daemon environment only.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-15.

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// authExtra builds a Config.Extra tree holding one harness table.
func authExtra(harness string, table map[string]any) map[string]any {
	return map[string]any{"agents": map[string]any{"auth": map[string]any{harness: table}}}
}

// subOnly offers subscription alone: no key class at all.
func subOnly() Binding {
	b := fakeBinding("subonly")
	b.Credentials = []CredentialSpec{{Class: CredentialSubscription}}
	return b
}

func wantInvalid(t *testing.T, err error, parts ...string) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("err = %q, want it to name %q", err, p)
		}
	}
}

func TestParseDriverAuthRefusesVarOutsideHarnessTable(t *testing.T) {
	bs := []Binding{fakeBinding("fake")}
	for _, name := range []string{"vendor_auth_var", "key_var", "config_dir_var", "env"} {
		for _, val := range []string{"LD_PRELOAD", "HOME", "FAKE_KEY"} {
			extra := authExtra("fake", map[string]any{"class": "api_key", "key_env": "FAKE_SRC", name: val})
			_, err := ParseDriverAuth(extra, bs)
			wantInvalid(t, err, "unknown key", name)
		}
	}
	// the one accepted shape still parses, and its variable comes from the table.
	got, err := ParseDriverAuth(authExtra("fake", map[string]any{"class": "api_key", "key_env": "FAKE_SRC"}), bs)
	if err != nil || got["fake"].Class != CredentialAPIKey || got["fake"].KeyEnv != "FAKE_SRC" {
		t.Fatalf("ParseDriverAuth(valid) = %+v, %v", got, err)
	}
	if v, err := KeyVarFor(bs[0], got["fake"].Class); err != nil || v != "FAKE_KEY" {
		t.Fatalf("KeyVarFor = %q, %v, want FAKE_KEY from the table", v, err)
	}
}

func TestParseDriverAuthClassMustBeOffered(t *testing.T) {
	for _, class := range []string{CredentialAPIKey, CredentialOAuthToken, CredentialCompatKey, "root", "Subscription"} {
		extra := authExtra("subonly", map[string]any{"class": class, "key_env": "SRC_VAR"})
		_, err := ParseDriverAuth(extra, []Binding{subOnly()})
		wantInvalid(t, err, class, "not offered")
	}
	if _, err := KeyVarFor(subOnly(), CredentialAPIKey); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("KeyVarFor(unoffered class) = %v, want KindInvalidInput", err)
	}
	if v, err := KeyVarFor(subOnly(), CredentialSubscription); err != nil || v != "" {
		t.Fatalf("KeyVarFor(subscription) = %q, %v, want an empty variable", v, err)
	}
}

// absAuthSource is an absolute path on every host ("/a/b" is relative on
// windows, which has no root without a drive).
var absAuthSource = filepath.Join(os.TempDir(), "a", "b")

func TestParseDriverAuthRefusesMissingFields(t *testing.T) {
	bs := []Binding{fakeBinding("fake")}
	cases := map[string]struct {
		table map[string]any
		want  string
	}{
		"no class":               {map[string]any{"key_env": "SRC"}, "class is required"},
		"class not a string":     {map[string]any{"class": 7}, "class must be a non-empty string"},
		"empty class":            {map[string]any{"class": ""}, "class must be a non-empty string"},
		"key class sans key_env": {map[string]any{"class": "api_key"}, "key_env is required"},
		"key_env lowercase":      {map[string]any{"class": "api_key", "key_env": "my_key"}, "not an environment variable name"},
		"key_env control var":    {map[string]any{"class": "api_key", "key_env": "CASCADE_HOME"}, "daemon control variable"},
		"key class with source":  {map[string]any{"class": "api_key", "key_env": "SRC", "auth_source": absAuthSource}, "only allowed for class subscription"},
		"subscription sans src":  {map[string]any{"class": "subscription"}, "auth_source is required"},
		"subscription key_env":   {map[string]any{"class": "subscription", "auth_source": absAuthSource, "key_env": "SRC"}, "key_env is not allowed"},
		"relative auth_source":   {map[string]any{"class": "subscription", "auth_source": "rel/auth.json"}, "absolute path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDriverAuth(authExtra("fake", c.table), bs)
			wantInvalid(t, err, c.want, "fake")
		})
	}
	for name, extra := range map[string]map[string]any{
		"unknown harness":    authExtra("nosuch", map[string]any{"class": "api_key"}),
		"table not a table":  {"agents": map[string]any{"auth": map[string]any{"fake": "api_key"}}},
		"auth not a table":   {"agents": map[string]any{"auth": "x"}},
		"agents not a table": {"agents": []any{}},
	} {
		if _, err := ParseDriverAuth(extra, bs); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
	if got, err := ParseDriverAuth(map[string]any{}, bs); err != nil || len(got) != 0 {
		t.Fatalf("ParseDriverAuth(no section) = %v, %v, want empty", got, err)
	}
	sub, err := ParseDriverAuth(authExtra("fake", map[string]any{"class": "subscription", "auth_source": "/a/auth.json"}), bs)
	if err != nil || sub["fake"].AuthSource != "/a/auth.json" || sub["fake"].KeyEnv != "" {
		t.Fatalf("ParseDriverAuth(subscription) = %+v, %v", sub, err)
	}
}

// TestKeyClassValueFromDaemonEnvOnly: the key value comes from the daemon
// environment variable the config names (read through getenv, nothing else),
// and an unset variable is KindUnavailable naming the variable.
func TestKeyClassValueFromDaemonEnvOnly(t *testing.T) {
	b := fakeBinding("fake")
	a := DriverAuth{Harness: "fake", Class: CredentialAPIKey, KeyEnv: "MY_DAEMON_KEY"}
	var asked []string
	getenv := func(name string) string {
		asked = append(asked, name)
		if name == "MY_DAEMON_KEY" {
			return "synthetic-key-value-0001"
		}
		return ""
	}
	built, err := BuildDriverEnv(b, a, testProfile(), "job-1", "ptok", "lease-1", sixProxy(), getenv)
	if err != nil {
		t.Fatalf("BuildDriverEnv: %v", err)
	}
	if !envHas(built.Driver, "FAKE_KEY=synthetic-key-value-0001") {
		t.Fatalf("driver env lacks the key under the table's variable: %v", built.Driver)
	}
	for _, n := range asked {
		if n != "PATH" && n != "LANG" && n != "MY_DAEMON_KEY" {
			t.Fatalf("getenv was asked for %q: only PATH, LANG and the named key variable may be read", n)
		}
	}
	_, err = BuildDriverEnv(b, a, testProfile(), "job-1", "ptok", "lease-1", sixProxy(), func(string) string { return "" })
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "MY_DAEMON_KEY") {
		t.Fatalf("absent key env err = %v, want KindUnavailable naming MY_DAEMON_KEY", err)
	}
}
