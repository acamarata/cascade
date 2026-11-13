package graph

import (
	"errors"
	"fmt"
)

// Runner represents a node that can execute tasks. Production marks a host that serves live traffic; the registry
// that builds the Runner list sets it, so no host address is built into the code.
type Runner struct {
	ID         string
	Host       string
	Production bool
}

// ResolveRunner resolves a runner from reg by override, then id, then host.
// It returns an error if the resolved runner is marked Production or no match is found.
func ResolveRunner(reg []Runner, override, id, host string) (Runner, error) {
	var r Runner
	var found bool

	if override != "" {
		for _, c := range reg {
			if c.ID == override {
				r = c
				found = true
				break
			}
		}
	}

	if !found && id != "" {
		for _, c := range reg {
			if c.ID == id {
				r = c
				found = true
				break
			}
		}
	}

	if !found && host != "" {
		for _, c := range reg {
			if c.Host == host {
				r = c
				found = true
				break
			}
		}
	}

	if !found {
		return Runner{}, errors.New("no matching runner found")
	}

	if r.Production {
		return Runner{}, fmt.Errorf("production host %s refused", r.Host)
	}

	return r, nil
}
