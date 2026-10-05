//go:build capmap

package capmap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	register("TestCapmap_InitConfigInitAndSet", probeInitConfig)
	register("TestCapmap_DoctorBundle", probeDoctorBundle)
}

// probeInitConfig proves `init --yes --no-daemon` and `config set`.
// Authorization: a config write that breaks a rule (remote elevation with
// no helper key) is refused with the typed invalid-input error and leaves
// config.toml byte-identical; the file is owner-only. Routing: init's
// summary names the data directory it set up and `config path` names the
// file config set wrote. Side effect: the provider registry database and
// config.toml exist on disk (init does not create cascade.db; see the gap
// row for init). Result: config get reports the stored value as coming from the file.
func probeInitConfig(t *testing.T) {
	c := newCLI(t)
	out := c.mustOK("", "init", "--yes", "--no-daemon").stdout
	data := filepath.Join(c.home, ".cascade", "data")
	if !strings.Contains(out, "== cascade is set up") || !strings.Contains(out, data) {
		t.Fatalf("init summary does not report setup under %s:\n%s", data, out)
	}
	providers := filepath.Join(data, "providers.db")
	if fi, err := os.Stat(providers); err != nil || fi.Size() == 0 {
		t.Fatalf("provider registry %s: %v, want a non-empty file", providers, err)
	}
	c.mustOK("", "config", "set", "runtime.profile", `"server"`)
	cfg := configPath(t, c.mustOK("", "config", "path").stdout)
	fi, err := os.Stat(cfg)
	if err != nil || fi.Mode().Perm()&0o077 != 0 || !strings.HasPrefix(cfg, c.home) {
		t.Fatalf("config file %q: info %v err %v, want an owner-only file under the home", cfg, fi, err)
	}
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bad := c.run("", "config", "set", "elevation.allow_remote", "true", "--json")
	if e := decodeEnvelope(t, bad); e.OK || e.Error == nil || e.Error.Kind != "invalid-input" || bad.code != 2 {
		t.Fatalf("invalid config set = %+v exit %d, want a typed invalid-input refusal", e, bad.code)
	}
	if after, _ := os.ReadFile(cfg); !bytes.Equal(before, after) {
		t.Fatalf("a refused config set changed config.toml:\n%s\n--- was\n%s", after, before)
	}
	if got := c.mustOK("", "config", "get", "runtime.profile").stdout; !strings.Contains(got, "runtime.profile = server (file)") {
		t.Fatalf("config get = %q, want the stored value from the file", got)
	}
}

// configPath extracts the config file path from `config path` output, which
// prints one "name = path" line per location.
func configPath(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "config = "); ok {
			return v
		}
	}
	t.Fatalf("config path output has no config line: %q", out)
	return ""
}

// bundleMembers returns member name to content of a .tar.gz file.
func bundleMembers(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	members := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return members
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		members[h.Name] = string(b)
	}
}

// doctorChecks returns the exit code and the check name to status map from
// `doctor --json`. Doctor exits non-zero when any check warns, but still
// writes the full report, so the report is read whatever the exit code.
func (c *cli) doctorChecks() (int, map[string]string) {
	c.t.Helper()
	r := c.run("", "doctor", "--json")
	// A warning run writes the report envelope and then an error envelope
	// on stdout, so only the first document is the report.
	var e envelope
	if err := json.NewDecoder(strings.NewReader(r.stdout)).Decode(&e); err != nil {
		c.t.Fatalf("doctor stdout has no leading envelope: %v\n%s", err, r.stdout)
	}
	var d struct {
		Entries []struct {
			Name   string `json:"name"`
			Result struct {
				Status string `json:"Status"`
			} `json:"result"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil || !e.OK {
		c.t.Fatalf("doctor: ok=%v err=%v", e.OK, err)
	}
	checks := map[string]string{}
	for _, en := range d.Entries {
		checks[en.Name] = en.Result.Status
	}
	return r.code, checks
}

// probeDoctorBundle proves doctor reads the live daemon and writes a
// redacted bundle. Authorization: a vault secret stored before the run is
// in no bundle member. Routing: with the daemon up the subsystem_census
// check passes, and with it stopped the same check warns instead of
// passing. Side effect: the bundle file named in the result exists and
// holds the four report members. Result: the check list has at least the
// census and config-permissions checks.
func probeDoctorBundle(t *testing.T) {
	const secret = "capmap-doctor-secret-5521"
	c := newCLI(t)
	c.mustOK(secret, "vault", "set", "dk")
	c.startDaemon()
	code, checks := c.doctorChecks()
	if code != 0 {
		t.Fatalf("doctor with the daemon up exited %d (checks %v)", code, checks)
	}
	for _, name := range []string{"subsystem_census", "config-permissions"} {
		if checks[name] != "ok" {
			t.Fatalf("check %s = %q with the daemon up (all: %v)", name, checks[name], checks)
		}
	}
	e := decodeEnvelope(t, c.mustOK("", "doctor", "bundle", "--json"))
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(e.Data, &res); err != nil || res.Path == "" {
		t.Fatalf("doctor bundle result %s: %v", e.Data, err)
	}
	t.Cleanup(func() { _ = os.Remove(res.Path) })
	members := bundleMembers(t, res.Path)
	for _, name := range []string{"system_info.json", "resolved_config.json", "check_report.json", "daemon_logs.txt"} {
		if _, ok := members[name]; !ok {
			t.Fatalf("bundle members %v, want %s", members, name)
		}
	}
	for name, body := range members {
		if strings.Contains(body, secret) {
			t.Fatalf("bundle member %s holds the vault secret", name)
		}
	}
	c.stopDaemon()
	code, checks = c.doctorChecks()
	if checks["subsystem_census"] != "warn" || code == 0 {
		t.Fatalf("daemon stopped: subsystem_census = %q exit %d, want warn and a non-zero exit", checks["subsystem_census"], code)
	}
}
