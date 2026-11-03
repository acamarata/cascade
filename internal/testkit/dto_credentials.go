package testkit

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Purpose: a reusable reflection check that a wire DTO carries no
//   credential material and no internal fleet topology type. Callers pass
//   one sample value per DTO type; the check walks each type's structure.
// Inputs: a testing.TB and one or more sample values.
// Outputs: one t.Errorf per finding; zero samples is itself a failure
//   (fail closed: an empty call must never pass silently).
// Constraints: deterministic; the topology package is matched by import
//   path string, never imported, so testkit stays below internal/fleet.

// topologyPkgPath is the import path of the fleet topology package whose
// types must never appear in a DTO. Compared as a string on purpose.
const topologyPkgPath = "github.com/acamarata/cascade/internal/fleet/topology"

// credentialMarkers are matched against field names and json tag names
// after lowercasing and removing '_' and '-'.
var credentialMarkers = []string{
	"secret", "password", "passwd", "passphrase", "token", "apikey",
	"credential", "privatekey", "accesskey", "bearer", "authorization",
	"keyref",
}

// RequireCredentialFreeDTO fails t for every credential-like field name or
// json tag, and every internal/fleet/topology type, reachable from the
// types of samples. Calling it with no samples, or with a nil sample, fails.
func RequireCredentialFreeDTO(t testing.TB, samples ...any) {
	t.Helper()
	checkCredentialFreeDTO(t, topologyPkgPath, samples)
}

// checkCredentialFreeDTO is RequireCredentialFreeDTO with the forbidden
// package path injected, so tests can exercise the package rule with a
// stand-in type instead of importing the real topology package.
func checkCredentialFreeDTO(t testing.TB, forbiddenPkg string, samples []any) {
	t.Helper()
	if len(samples) == 0 {
		t.Errorf("RequireCredentialFreeDTO: no samples given; nothing was checked")
		return
	}
	for i, s := range samples {
		if s == nil {
			t.Errorf("RequireCredentialFreeDTO: sample %d is nil; its type cannot be checked", i)
			continue
		}
		typ := reflect.TypeOf(s)
		w := &dtoWalker{forbiddenPkg: forbiddenPkg, seen: map[reflect.Type]bool{}}
		w.walk(typ, typ.String())
		for _, f := range w.findings {
			t.Errorf("RequireCredentialFreeDTO: %s", f)
		}
	}
}

// dtoWalker collects findings over one sample's type graph.
type dtoWalker struct {
	forbiddenPkg string
	seen         map[reflect.Type]bool
	findings     []string
}

func (w *dtoWalker) walk(typ reflect.Type, path string) {
	if w.seen[typ] {
		return
	}
	w.seen[typ] = true
	if isForbiddenPkg(typ.PkgPath(), w.forbiddenPkg) {
		w.findings = append(w.findings, fmt.Sprintf("%s: type %s comes from %s", path, typ, w.forbiddenPkg))
	}
	switch typ.Kind() { //nolint:exhaustive // only composite kinds reach further types
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		w.walk(typ.Elem(), path+"[elem]")
	case reflect.Map:
		w.walk(typ.Key(), path+"[key]")
		w.walk(typ.Elem(), path+"[value]")
	case reflect.Struct:
		for i := range typ.NumField() {
			w.field(typ.Field(i), path)
		}
	}
}

func (w *dtoWalker) field(f reflect.StructField, parent string) {
	path := parent + "." + f.Name
	if m := credentialMarker(f.Name); m != "" {
		w.findings = append(w.findings, fmt.Sprintf("%s: field name looks like a credential (%q)", path, m))
	}
	if name := jsonTagName(f.Tag); name != "" {
		if m := credentialMarker(name); m != "" {
			w.findings = append(w.findings, fmt.Sprintf("%s: json tag %q looks like a credential (%q)", path, name, m))
		}
	}
	w.walk(f.Type, path)
}

// jsonTagName returns the name part of a field's json tag, or "".
func jsonTagName(tag reflect.StructTag) string {
	v, ok := tag.Lookup("json")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(v, ",")
	if name == "-" {
		return ""
	}
	return name
}

// credentialMarker returns the first credential marker found in s, or "".
func credentialMarker(s string) string {
	n := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(s))
	for _, m := range credentialMarkers {
		if strings.Contains(n, m) {
			return m
		}
	}
	return ""
}

// isForbiddenPkg reports whether pkg is forbidden or one of its subpackages.
func isForbiddenPkg(pkg, forbidden string) bool {
	if pkg == "" || forbidden == "" {
		return false
	}
	return pkg == forbidden || strings.HasPrefix(pkg, forbidden+"/")
}
