package graph

import (
	"errors"
	"fmt"
)

// Runner represents a node that can execute tasks.
type Runner struct {
	ID   string
	Host string
}

var productionHosts = map[string]bool{
	"5.75.235.42":     true,
	"167.235.195.186": true,
	"159.69.190.92":   true,
}

// ResolveRunner resolves a runner from reg by override, then id, then host.
// It returns an error if the resolved runner has a production host or no match is found.
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

	if productionHosts[r.Host] {
		return Runner{}, fmt.Errorf("production host %s refused", r.Host)
	}

	return r, nil
}
