package testkit

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// dtoRecorder is a non-halting testing.TB that records Errorf messages so
// RequireCredentialFreeDTO's failure paths can be asserted without failing
// the outer test.
type dtoRecorder struct {
	testing.TB
	errs []string
}

func (r *dtoRecorder) Helper() {}

func (r *dtoRecorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *dtoRecorder) Fatalf(format string, args ...any) { r.Errorf(format, args...) }

type cleanDTO struct {
	ID     string            `json:"id"`
	Count  int               `json:"count"`
	Labels map[string]string `json:"labels,omitempty"`
	Nested *cleanNested      `json:"nested"`
}

type cleanNested struct {
	Host string   `json:"host"`
	Tags []string `json:"tags"`
}

type secretRefDTO struct {
	ID        string `json:"id"`
	SecretRef string `json:"ref"`
}

type apiKeyTagDTO struct {
	Name string `json:"name"`
	Key  string `json:"api_key"`
}

// fakeTopologyNode stands in for an internal/fleet/topology type; the test
// points the forbidden package path at testkit itself.
type fakeTopologyNode struct {
	Zone string `json:"zone"`
}

func TestRequireCredentialFreeDTO(t *testing.T) {
	t.Run("clean sample passes", func(t *testing.T) {
		rec := &dtoRecorder{TB: t}
		RequireCredentialFreeDTO(rec, cleanDTO{}, &cleanNested{})
		if len(rec.errs) != 0 {
			t.Fatalf("clean samples: want no failures, got %q", rec.errs)
		}
	})
	t.Run("SecretRef field fails", func(t *testing.T) {
		requireFinding(t, []any{secretRefDTO{}}, topologyPkgPath, "SecretRef")
	})
	t.Run("api_key json tag fails", func(t *testing.T) {
		requireFinding(t, []any{apiKeyTagDTO{}}, topologyPkgPath, `"api_key"`)
	})
	t.Run("nested credential behind a slice of pointers fails", func(t *testing.T) {
		requireFinding(t, []any{struct{ Items []*apiKeyTagDTO }{}}, topologyPkgPath, "Items[elem][elem].Key")
	})
	t.Run("embedded topology type fails", func(t *testing.T) {
		stand := reflect.TypeOf(fakeTopologyNode{}).PkgPath()
		sample := struct {
			fakeTopologyNode
			Name string `json:"name"`
		}{}
		requireFinding(t, []any{sample}, stand, ".fakeTopologyNode: type testkit.fakeTopologyNode")
	})
	t.Run("topology path matching", func(t *testing.T) {
		cases := map[string]bool{
			topologyPkgPath:                      true,
			topologyPkgPath + "/sub":             true,
			topologyPkgPath + "x":                false,
			"github.com/acamarata/cascade/fleet": false,
			"":                                   false,
		}
		for pkg, want := range cases {
			if got := isForbiddenPkg(pkg, topologyPkgPath); got != want {
				t.Errorf("isForbiddenPkg(%q) = %v, want %v", pkg, got, want)
			}
		}
	})
	t.Run("zero samples fails", func(t *testing.T) {
		rec := &dtoRecorder{TB: t}
		RequireCredentialFreeDTO(rec)
		if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "no samples") {
			t.Fatalf("zero samples: want one 'no samples' failure, got %q", rec.errs)
		}
	})
	t.Run("nil sample fails", func(t *testing.T) {
		requireFinding(t, []any{nil}, topologyPkgPath, "sample 0 is nil")
	})
}

// TestRequireCredentialFreeDTOContractMarkers pins the contract's marker
// list: an authorization field name and a key_ref json tag both fail.
func TestRequireCredentialFreeDTOContractMarkers(t *testing.T) {
	t.Run("authorization field fails", func(t *testing.T) {
		requireFinding(t, []any{struct{ Authorization string }{}}, topologyPkgPath, `("authorization")`)
	})
	t.Run("key_ref json tag fails", func(t *testing.T) {
		requireFinding(t, []any{struct {
			Ref string `json:"key_ref"`
		}{}}, topologyPkgPath, `json tag "key_ref"`)
	})
	t.Run("a json dash tag names nothing", func(t *testing.T) {
		rec := &dtoRecorder{TB: t}
		RequireCredentialFreeDTO(rec, struct {
			Name string `json:"-"`
		}{})
		if len(rec.errs) != 0 {
			t.Fatalf("json:\"-\": want no failures, got %q", rec.errs)
		}
	})
}

// requireFinding runs the check on samples and asserts at least one
// recorded failure contains want.
func requireFinding(t *testing.T, samples []any, forbidden, want string) {
	t.Helper()
	rec := &dtoRecorder{TB: t}
	checkCredentialFreeDTO(rec, forbidden, samples)
	for _, e := range rec.errs {
		if strings.Contains(e, want) {
			return
		}
	}
	t.Fatalf("want a failure containing %q, got %q", want, rec.errs)
}
