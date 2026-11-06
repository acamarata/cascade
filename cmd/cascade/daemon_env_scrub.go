// Purpose: the daemon's start-time credential scrub (P1-GWY-10). The first
// thing composeDaemon does after reading config.toml is snapshot every gateway
// and credential variable of its own environment and unset it, so no child
// the daemon starts inherits a token by accident and later code reads the
// values only through the private snapshot (credentialEnv.Getenv).
//
// Constraints: the variable names come from the Binding rows and the on-disk
// config only, never from a literal here. A value never enters an error, a log
// line or a formatted string: every formatter of credentialEnv prints
// `[redacted n=<count>]`. credentialEnv is safe for concurrent use.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/runtime"
)

// credentialNames returns every environment variable name the daemon scrubs
// at start: every KeyVar of the claude Binding rows, every key_env of an
// on-disk [gateways.*] account and every key_env of [agents.auth.*]. The
// result is sorted and free of duplicates. A family that does not decode
// still leaves the other names in the list, so a bad section never stops
// the scrub of the rest.
func credentialNames(onDisk runtime.EffectiveConfig) []string {
	set := map[string]bool{}
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	for _, b := range agents.Bindings() {
		if b.Harness != agents.HarnessClaude {
			continue
		}
		for _, c := range b.Credentials {
			add(c.KeyVar)
		}
	}
	if accts, err := gateway.AccountsFrom(onDisk); err == nil {
		for _, a := range accts {
			if a.Credential.Kind == gateway.CredentialEnv {
				add(a.Credential.EnvVar)
			}
		}
	}
	for _, n := range authKeyEnvs(onDisk) {
		add(n)
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// authKeyEnvs returns the key_env of every [agents.auth.*] table in the
// admitted family, sorted.
func authKeyEnvs(eff runtime.EffectiveConfig) []string {
	raw := eff.Families["agents.auth"]
	if len(raw) == 0 {
		return nil
	}
	var tables map[string]struct {
		KeyEnv string `json:"key_env"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil
	}
	var out []string
	for _, t := range tables {
		if t.KeyEnv != "" {
			out = append(out, t.KeyEnv)
		}
	}
	sort.Strings(out)
	return out
}

// admittedKeyEnvs returns the key_env names the on-disk config admits: the
// gateway accounts' and [agents.auth.*]'s. A scrubbed name outside this list
// keeps no value in the snapshot after Admit.
func admittedKeyEnvs(onDisk runtime.EffectiveConfig) []string {
	out := authKeyEnvs(onDisk)
	if accts, err := gateway.AccountsFrom(onDisk); err == nil {
		for _, a := range accts {
			if a.Credential.Kind == gateway.CredentialEnv && a.Credential.EnvVar != "" {
				out = append(out, a.Credential.EnvVar)
			}
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// credentialEnv is the daemon-private snapshot of the scrubbed variables.
// scrubbed holds every name the scrub owns; values holds the snapshot value
// of each one that was set; admitted marks the names whose value survives.
type credentialEnv struct {
	mu       sync.RWMutex
	scrubbed map[string]bool
	values   map[string]string
	admitted map[string]bool
}

// scrubCredentialEnv snapshots every name in names that lookup finds set,
// then unsets it through unset. It returns the snapshot and the names it
// removed, sorted. A name unset refuses stays in the snapshot and out of
// removed; the caller sees it is still set by its absence from removed.
func scrubCredentialEnv(names []string, lookup func(string) (string, bool), unset func(string) error) (*credentialEnv, []string) {
	c := &credentialEnv{scrubbed: map[string]bool{}, values: map[string]string{}, admitted: map[string]bool{}}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c, c.scrubLocked(names, lookup, unset)
}

func (c *credentialEnv) scrubLocked(names []string, lookup func(string) (string, bool), unset func(string) error) []string {
	var removed []string
	for _, n := range names {
		if n == "" {
			continue
		}
		c.scrubbed[n] = true
		v, ok := lookup(n)
		if !ok {
			continue
		}
		c.values[n] = v
		if unset(n) == nil {
			removed = append(removed, n)
		}
	}
	sort.Strings(removed)
	return slices.Compact(removed)
}

// Admit marks names as admitted key_env names. After admission the snapshot
// value of every scrubbed name that names does not list is dropped.
func (c *credentialEnv) Admit(names []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		if c.scrubbed[n] {
			c.admitted[n] = true
		}
	}
	for n := range c.values {
		if !c.admitted[n] {
			delete(c.values, n)
		}
	}
}

// Getenv returns a scrubbed name's snapshot value when it is admitted and ""
// otherwise; any other name is read from the process environment.
func (c *credentialEnv) Getenv(name string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.scrubbed[name] {
		if !c.admitted[name] {
			return ""
		}
		return c.values[name]
	}
	return os.Getenv(name)
}

// RelaunchEnviron returns the environment of an upgrade relaunch: the
// current (scrubbed) process environment plus every admitted snapshot entry.
// The relaunched daemon scrubs again at its own start.
func (c *credentialEnv) RelaunchEnviron() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	env := os.Environ()
	names := make([]string, 0, len(c.values))
	for n := range c.values {
		if c.admitted[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		env = append(env, n+"="+c.values[n])
	}
	return env
}

func (c *credentialEnv) count() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.values)
}

func (c *credentialEnv) redacted() string { return fmt.Sprintf("[redacted n=%d]", c.count()) }

// String prints only the redaction marker and the snapshot size.
func (c *credentialEnv) String() string { return c.redacted() }

// GoString prints only the redaction marker, for %#v.
func (c *credentialEnv) GoString() string { return c.redacted() }

// Format prints the redaction marker for every verb, so no fmt verb reaches
// the snapshot values.
func (c *credentialEnv) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(c.redacted())) }

// LogValue prints the redaction marker in slog output.
func (c *credentialEnv) LogValue() slog.Value { return slog.StringValue(c.redacted()) }

// scrubDaemonEnv snapshots and unsets every gateway and credential variable
// before anything is opened, admitted or spawned; the daemon's later reads go
// through the snapshot's Getenv only. It returns the snapshot and the
// removed names.
func scrubDaemonEnv(cfg *runtime.Config, deps *daemonDeps) (*credentialEnv, []string) {
	onDisk := runtime.EffectiveConfigOf(cfg)
	creds, removed := scrubCredentialEnv(credentialNames(onDisk), os.LookupEnv, os.Unsetenv)
	creds.Admit(admittedKeyEnvs(onDisk))
	deps.Getenv = creds.Getenv
	return creds, removed
}
