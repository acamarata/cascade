//go:build postgres

// Purpose: proves no fmt verb prints a Driver's secret set. The set holds
// the raw DSN and every plaintext password spelling, so %v, %+v, %#v and %s
// on a Driver value, a *Driver and a struct embedding one must all leave
// the canary out. Nothing is dialed; the canary is built at run time and
// never printed.
package pgvector

import (
	"fmt"
	"strings"
	"testing"
)

// formatHolder embeds a Driver both exported and unexported, the two ways
// a caller's struct can end up in a log line.
type formatHolder struct {
	Exported Driver
	inner    *Driver
}

func TestDriverFormattingNeverPrintsSecrets(t *testing.T) {
	isolatePGEnv(t)
	dsn := "postgres://u:" + canary() + "@127.0.0.1:1/db"
	forms, ok := dsnSecrets(dsn)
	if !ok || len(forms) == 0 {
		t.Fatal("dsnSecrets did not parse the fixture DSN")
	}
	d := &Driver{secrets: newSecretSet(forms)}
	subjects := map[string]any{
		"ptr":              d,
		"value":            *d,
		"set-ptr":          d.secrets,
		"set-value":        *d.secrets,
		"holder":           formatHolder{Exported: *d, inner: d},
		"holder-ptr":       &formatHolder{Exported: *d, inner: d},
		"slice-of-drivers": []Driver{*d},
		"map-of-ptrs":      map[string]*Driver{"k": d},
	}
	for name, v := range subjects {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(verb, v); strings.Contains(got, canary()) {
				t.Errorf("%s with %s printed the secret set", name, verb)
			}
		}
		if got := fmt.Sprint(v); strings.Contains(got, canary()) {
			t.Errorf("%s with Sprint printed the secret set", name)
		}
	}
	if got := d.secrets.list(); len(got) != len(forms) {
		t.Fatalf("list() = %d forms, want %d", len(got), len(forms))
	}
	var unknown *secretSet
	if unknown.list() != nil || newSecretSet(nil) != nil {
		t.Fatal("the unknown set must be nil and list nothing")
	}
}
