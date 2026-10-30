package gateway

import "testing"

func TestGatewaysFamilyLoosening(t *testing.T) {
	a := Account{Name: "alpha", ID: "https://gateway.invalid", BaseURL: "https://gateway.invalid", Header: HeaderBearer,
		Credential: Credential{Kind: CredentialHelper, HelperPath: "/owner/program", HelperSHA256: "pin"},
		Models:     []string{"model-a"}, Usage: UsageSource{Kind: "endpoint", Window: "day", Path: "/usage"}, TermsSensitive: true}
	changes := []struct {
		key    string
		change func(*Account)
	}{
		{"base_url", func(a *Account) { a.BaseURL += "/other" }},
		{"header", func(a *Account) { a.Header = HeaderXAPIKey }},
		{"credential_helper", func(a *Account) { a.Credential.HelperPath += "-other" }},
		{"helper_sha256", func(a *Account) { a.Credential.HelperSHA256 = "other" }},
		{"models", func(a *Account) { a.Models = []string{"model-b"} }},
		{"usage.window", func(a *Account) { a.Usage.Window = "week" }},
	}
	for _, c := range changes {
		t.Run(c.key, func(t *testing.T) {
			b := a
			c.change(&b)
			p := Compare([]Account{a}, []Account{b})
			if len(p) != 1 || p[0].Family != "gateways" || p[0].Key != "alpha."+c.key {
				t.Fatalf("wrong loosening: %v", p)
			}
		})
	}
	env := a
	env.Credential = Credential{Kind: CredentialEnv, EnvVar: "TEST_KEY"}
	next := env
	next.Credential.EnvVar = "OTHER_KEY"
	if p := Compare([]Account{env}, []Account{next}); len(p) != 1 || p[0].Key != "alpha.key_env" {
		t.Fatalf("env change: %v", p)
	}
	if p := Compare(nil, []Account{a}); len(p) != 1 {
		t.Fatalf("addition: %v", p)
	}
	if p := Compare([]Account{a}, nil); len(p) != 0 {
		t.Fatalf("removal: %v", p)
	}
	if p := Compare([]Account{a}, []Account{a}); len(p) != 0 {
		t.Fatalf("identical: %v", p)
	}
}

func TestCompareUsageFields(t *testing.T) {
	changes := []func(*UsageSource){
		func(u *UsageSource) { u.Kind = "headers" }, func(u *UsageSource) { u.Path = "/other" },
		func(u *UsageSource) { u.UsedPointer = "/used" }, func(u *UsageSource) { u.LimitPointer = "/limit" },
		func(u *UsageSource) { u.UtilizationPointer = "/util" }, func(u *UsageSource) { u.ResetsAtPointer = "/reset" },
		func(u *UsageSource) { u.UtilizationHeader = "x-used" }, func(u *UsageSource) { u.ResetsHeader = "x-resets" },
	}
	for _, change := range changes {
		a := Account{Name: "alpha"}
		b := a
		change(&b.Usage)
		if p := Compare([]Account{a}, []Account{b}); len(p) != 1 {
			t.Fatalf("usage change: %v", p)
		}
	}
	a := Account{Name: "alpha", Credential: Credential{Kind: CredentialEnv}}
	b := a
	b.Credential.Kind = CredentialHelper
	if p := Compare([]Account{a}, []Account{b}); len(p) != 1 {
		t.Fatalf("kind change: %v", p)
	}
}

func TestCompareOrdersPathsAndModelShape(t *testing.T) {
	a := Account{Name: "alpha", BaseURL: "https://a.invalid", Models: []string{"m"}}
	z := Account{Name: "zulu", BaseURL: "https://z.invalid"}
	p := Compare(nil, []Account{z, a})
	if len(p) != 2 || p[0].Key != "alpha" || p[1].Key != "zulu" {
		t.Fatalf("added accounts not ordered by key: %v", p)
	}
	for name, models := range map[string][]string{
		"declared to discovered": nil,
		"longer list":            {"m", "n"},
	} {
		next := a
		next.Models = models
		if p := Compare([]Account{a}, []Account{next}); len(p) != 1 || p[0].Key != "alpha.models" {
			t.Fatalf("%s: %v", name, p)
		}
	}
}
