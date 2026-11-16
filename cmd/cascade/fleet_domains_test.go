package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubDomainsDo replaces the daemon call with a canned fleet.domains reply.
func stubDomainsDo(t *testing.T, reply string) *[]string {
	t.Helper()
	var methods []string
	prev := fleetDomainsDo
	fleetDomainsDo = func(_ context.Context, _ fleetSessionsDeps, method string, _, out any) error {
		methods = append(methods, method)
		return json.Unmarshal([]byte(reply), out)
	}
	t.Cleanup(func() { fleetDomainsDo = prev })
	return &methods
}

// runFleetDomains runs `fleet domains <args>` and returns stdout.
func runFleetDomains(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newFleetDomainsCmd(fleetSessionsDeps{})
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestFleetDomainsEmptyTopologyCLI(t *testing.T) {
	methods := stubDomainsDo(t, `{"version":"1","data":[]}`)
	out, err := runFleetDomains(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "ID") {
		t.Fatalf("empty list = %q, want the header only", out)
	}
	jsonOut, err := runFleetDomains(t, "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var env struct {
		Version int             `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, jsonOut)
	}
	if !env.OK || string(env.Data) != "[]" {
		t.Fatalf("envelope = %s, want ok with data []", jsonOut)
	}
	if len(*methods) != 2 || (*methods)[0] != "fleet.domains.list" {
		t.Fatalf("methods = %v", *methods)
	}
}

func TestFleetDomainsShowRendersBuckets(t *testing.T) {
	stubDomainsDo(t, `{"version":"1","data":{"id":"d1","kind":"api","billing_tier":"discover","holders":2,`+
		`"buckets":[{"name":"rpm","window":"1m","source":"headers","limit":50,"remaining_fraction":0.5,"confidence":1}]}}`)
	out, err := runFleetDomains(t, "show", "d1")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"d1", "discover", "rpm", "0.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	if _, err := runFleetDomains(t, "show"); err == nil {
		t.Error("show without an id: want a usage error")
	}
}

func TestFleetDomainsRefusesUnknownVersion(t *testing.T) {
	stubDomainsDo(t, `{"version":"2","data":[]}`)
	if _, err := runFleetDomains(t, "list"); err == nil {
		t.Fatal("version 2 reply: want an error")
	}
}

func TestFleetDomainsNoInputIdentical(t *testing.T) {
	stubDomainsDo(t, `{"version":"1","data":[{"id":"d1","kind":"api","billing_tier":"paid","holders":1,"buckets":[]}]}`)
	a, _ := runFleetDomains(t, "list")
	t.Setenv("CASCADE_NO_INPUT", "1")
	b, _ := runFleetDomains(t, "list")
	if a != b || !strings.Contains(a, "d1") {
		t.Fatalf("outputs differ or empty:\n%q\n%q", a, b)
	}
}
