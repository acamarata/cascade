// Purpose: parses the operator's [agents.auth.<harness>] tables against the
// closed per-harness credential table, so config chooses a credential class
// and a source and never a variable name.
// Inputs: the raw Config.Extra tree and the registered bindings.
// Outputs: one DriverAuth per configured harness, or KindInvalidInput naming
// the offending harness, key or class.
// Constraints: the accepted keys are exactly class, key_env and auth_source
// (anything else, vendor_auth_var included, is refused as an unknown key);
// the class must be one the harness's table offers; key_env is the NAME of a
// daemon environment variable holding the value (key classes only) and may
// not name a CASCADE_ control variable; auth_source is an absolute path
// (subscription only). Nothing here reads the environment, the vault or the
// file: the value is read later, from the daemon environment only (E.1).
// SPORT: internal.providers.agents/ADD (P1-AGT-15, contract:driver-credentials).

package agents

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DriverAuth is one harness's validated credential choice.
type DriverAuth struct {
	Harness HarnessID
	// Class is one of the harness's CredentialSpec classes.
	Class string
	// KeyEnv names the daemon environment variable that holds the value.
	// It is empty for subscription.
	KeyEnv string
	// AuthSource is the absolute vendor auth file path, linked in place.
	// It is empty for every class but subscription.
	AuthSource string
}

// daemonVarPrefix marks the daemon's own control variables, never a key source.
const daemonVarPrefix = "CASCADE_"

// ParseDriverAuth reads extra["agents"]["auth"], one table per harness, and
// validates each against its binding. An absent section is an empty result.
func ParseDriverAuth(extra map[string]any, bindings []Binding) (map[HarnessID]DriverAuth, error) {
	out := map[HarnessID]DriverAuth{}
	tables, err := authTables(extra)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		bi := slices.IndexFunc(bindings, func(b Binding) bool { return string(b.Harness) == name })
		if bi < 0 {
			return nil, cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: %q is not a registered harness", name, name)
		}
		table, ok := tables[name].(map[string]any)
		if !ok {
			return nil, cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s] must be a table", name)
		}
		a, err := parseAuthTable(bindings[bi], table)
		if err != nil {
			return nil, err
		}
		out[a.Harness] = a
	}
	return out, nil
}

// authTables digs the harness tables out of the raw tree.
func authTables(extra map[string]any) (map[string]any, error) {
	section, ok := extra["agents"]
	if !ok {
		return nil, nil
	}
	agents, ok := section.(map[string]any)
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "agents: [agents] must be a table")
	}
	raw, ok := agents["auth"]
	if !ok {
		return nil, nil
	}
	tables, ok := raw.(map[string]any)
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "agents: [agents.auth] must be a table")
	}
	return tables, nil
}

// parseAuthTable validates one harness table.
func parseAuthTable(b Binding, table map[string]any) (DriverAuth, error) {
	for key := range table {
		if key != "class" && key != "key_env" && key != "auth_source" {
			return DriverAuth{}, cascade.Newf(cascade.KindInvalidInput,
				"agents: [agents.auth.%s]: unknown key %q (accepted: class, key_env, auth_source; a variable name never comes from config)", b.Harness, key)
		}
	}
	a := DriverAuth{Harness: b.Harness}
	var err error
	if a.Class, err = authString(b, table, "class"); err != nil {
		return DriverAuth{}, err
	}
	if a.Class == "" {
		return DriverAuth{}, cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: class is required", b.Harness)
	}
	if _, err = KeyVarFor(b, a.Class); err != nil {
		return DriverAuth{}, err
	}
	if a.KeyEnv, err = authString(b, table, "key_env"); err != nil {
		return DriverAuth{}, err
	}
	if a.AuthSource, err = authString(b, table, "auth_source"); err != nil {
		return DriverAuth{}, err
	}
	return a, checkAuthShape(b, a)
}

// authString returns table[key] as a string; a missing key is "".
func authString(b Binding, table map[string]any, key string) (string, error) {
	v, ok := table[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: %s must be a non-empty string", b.Harness, key)
	}
	return s, nil
}

// checkAuthShape enforces which fields each class requires or forbids.
func checkAuthShape(b Binding, a DriverAuth) error {
	if a.Class == CredentialSubscription {
		switch {
		case a.KeyEnv != "":
			return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: key_env is not allowed for class subscription", b.Harness)
		case a.AuthSource == "":
			return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: auth_source is required for class subscription", b.Harness)
		case !filepath.IsAbs(a.AuthSource):
			return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: auth_source must be an absolute path", b.Harness)
		}
		return nil
	}
	switch {
	case a.AuthSource != "":
		return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: auth_source is only allowed for class subscription", b.Harness)
	case a.KeyEnv == "":
		return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: key_env is required for class %s", b.Harness, a.Class)
	case !envVarName.MatchString(a.KeyEnv):
		return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: key_env %q is not an environment variable name", b.Harness, a.KeyEnv)
	case strings.HasPrefix(a.KeyEnv, daemonVarPrefix):
		return cascade.Newf(cascade.KindInvalidInput, "agents: [agents.auth.%s]: key_env %q names a daemon control variable", b.Harness, a.KeyEnv)
	}
	return nil
}

// KeyVarFor returns the child-side variable that carries class for b, from
// the binding's table only; "" for subscription. A class the table does not
// offer is KindInvalidInput.
func KeyVarFor(b Binding, class string) (string, error) {
	for _, c := range b.Credentials {
		if c.Class == class {
			return c.KeyVar, nil
		}
	}
	return "", cascade.Newf(cascade.KindInvalidInput, "agents: %s: credential class %q is not offered by this harness", b.Harness, class)
}
