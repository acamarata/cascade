package main

// Purpose: the production half of P1-SEC-30: the cascade binary installs
// the process detector as the config-value scanner, and acceptConfigLoosening
// returns a dial failure as is. The callElevated round trips are in
// mount_config_elevated_test.go (integration tag, beside its harness).

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCascadeInstallsProcessSecretScanner(t *testing.T) {
	// "Bearer" plus a three-part token: the HEAD heuristic alone admits it
	// (it has a space), so only the installed detector refuses it.
	v := "Bearer " + "eyJ" + strings.Repeat("Qx7", 4) + "." + strings.Repeat("Lm4", 4) + "." + strings.Repeat("Tz9", 4)
	if bad, _ := runtime.LooksLikeSecret(v); bad {
		t.Fatal("fixture is refused by the heuristic alone; it cannot prove the scanner")
	}
	var hits *runtime.SecretLiteralsError
	err := runtime.ScanTreeForSecrets(map[string]interface{}{"x": map[string]interface{}{"y": v}})
	if !errors.As(err, &hits) || len(hits.Hits) != 1 {
		t.Fatalf("err = %v; want the installed process detector to refuse x.y", err)
	}
	if kind, _ := cascade.KindOf(err); kind == cascade.KindUnavailable {
		t.Fatal("no scanner installed in the cascade binary")
	}
}

func TestAcceptConfigLooseningDialFailureIsReturned(t *testing.T) {
	want := cascade.New(cascade.KindUnavailable, "no daemon socket")
	err := acceptConfigLoosening(t.Context(), func() (*client.Client, error) { return nil, want }, elevatedCallDeps{}, "")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the dial failure", err)
	}
}
