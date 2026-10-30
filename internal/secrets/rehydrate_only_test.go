// Purpose: the RehydrateOnly guard. A tag the caller's allow predicate
//
//	refuses refuses the whole content before the first vault read, the
//	names RehydrationTagNames reports are exactly the spans the guard is
//	asked about and resolves, and RehydrateOnly is the Rehydrator's only
//	exported method.
//
// Inputs: an in-memory custody behind a read counter; the FuzzRehydrateScan
//
//	seed corpus. No keychain, no HOME.
//
// SPORT: REHYDRATE_ONLY tests.
package secrets

import (
	"context"
	"errors"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/acamarata/cascade/pkg/cascade"
)

// realHome is the home directory before any test redirects it: the
// isolation guard below refuses a test that still points at it.
var realHome, _ = os.UserHomeDir()

// isolateHome points HOME, USERPROFILE and CASCADE_HOME into a fresh temp
// dir and fails the test if the home directory still resolves to the real
// one, so no fixture here can reach a keychain or a real config.
func isolateHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, dir)
	}
	requireIsolatedHome(t)
}

func requireIsolatedHome(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || realHome == "" || home == realHome || strings.HasPrefix(home, realHome+string(filepath.Separator)) {
		t.Fatalf("test home %q is not isolated from the real home (err %v)", home, err)
	}
}

// readCountingCustody counts every Get and answers any name with fixed unless
// the inner custody is set, in which case it answers from that.
type readCountingCustody struct {
	*memCustody
	reads []string
	fixed []byte
}

func (c *readCountingCustody) Get(ctx context.Context, name string) ([]byte, error) {
	c.reads = append(c.reads, name)
	if c.fixed != nil {
		return append([]byte(nil), c.fixed...), nil
	}
	return c.memCustody.Get(ctx, name)
}

func countingRehydrator(t *testing.T, entries map[string]string) (*Rehydrator, *readCountingCustody) {
	t.Helper()
	isolateHome(t)
	c := &readCountingCustody{memCustody: newMemCustody()}
	for name, value := range entries {
		if err := c.Set(context.Background(), name, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	b, err := NewBroker(c, &refusingGate{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRehydrator(b)
	if err != nil {
		t.Fatal(err)
	}
	return r, c
}

func allowNames(names ...string) func(Tag) bool {
	return func(tag Tag) bool { return slices.Contains(names, tag.Name) }
}

const wantNotPermitted = "permission-denied: secrets: tag not permitted at this boundary"

func TestRehydrateOnlyRefusesDisallowedBeforeVaultRead(t *testing.T) {
	ctx := context.Background()
	r, c := countingRehydrator(t, map[string]string{"VAULT_A": "resolved-a", "VAULT_B": "resolved-b"})
	content := []byte("first <apikey>VAULT_A</apikey> then <token>VAULT_B</token>")
	for name, allow := range map[string]func(Tag) bool{"one of two": allowNames("VAULT_A"), "nil": nil, "none": allowNames()} {
		rc, err := r.RehydrateOnly(ctx, content, allow)
		if rc != nil || err != error(ErrTagNotPermitted) || err.Error() != wantNotPermitted || !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("%s: rc=%v err=%v, want the tag-not-permitted refusal", name, rc, err)
		}
		if len(c.reads) != 0 {
			t.Fatalf("%s: the vault was read %d times before the refusal", name, len(c.reads))
		}
	}
	rc, err := r.RehydrateOnly(ctx, content, allowNames("VAULT_A", "VAULT_B"))
	if err != nil || string(rc.Data) != "first resolved-a then resolved-b" || len(c.reads) != 2 {
		t.Fatalf("all permitted: rc=%v err=%v reads=%v", rc, err, c.reads)
	}
	rc.Zero()
	rc, err = r.RehydrateOnly(ctx, []byte("<apikey>NO_SUCH_KEY</apikey>"), allowNames("NO_SUCH_KEY"))
	if rc != nil || !errors.Is(err, ErrVaultKeyNotFound) || !cascade.HasKind(err, cascade.KindNotFound) ||
		!strings.HasPrefix(err.Error(), "not-found: secrets: rehydrating <NO_SUCH_KEY>") {
		t.Fatalf("permitted unknown name: rc=%v err=%v, want ErrVaultKeyNotFound", rc, err)
	}
	if rc, err := r.RehydrateOnly(ctx, []byte("no tags here"), nil); err != nil || string(rc.Data) != "no tags here" {
		t.Fatalf("untagged content with nil allow: rc=%v err=%v", rc, err)
	}
}

// corpusInputs decodes the FuzzRehydrateScan seed files (go test fuzz v1,
// one []byte literal each).
func corpusInputs(t *testing.T) [][]byte {
	t.Helper()
	dir := filepath.Join("testdata", "fuzz", "FuzzRehydrateScan")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("seed corpus: %d entries, %v", len(entries), err)
	}
	var out [][]byte
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		_, literal, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
		body, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(literal, "[]byte("), ")"))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		out = append(out, []byte(body))
	}
	return out
}

func TestRehydrationTagNamesMatchesRehydrateSpans(t *testing.T) {
	inputs := append(corpusInputs(t), []byte("<apikey>A</apikey><apikey>A</apikey>"), []byte("<token>X</tok"), nil)
	resolved := 0
	for _, in := range inputs {
		tags, terr := RehydrationTagNames(in)
		r, c := countingRehydrator(t, nil)
		c.fixed = []byte("v")
		var asked []Tag
		rc, rerr := r.RehydrateOnly(context.Background(), in, func(tag Tag) bool { asked = append(asked, tag); return true })
		if (terr == nil) != (rerr == nil) {
			t.Fatalf("%q: names err %v, rehydrate err %v", in, terr, rerr)
		}
		if terr != nil {
			if !errors.Is(terr, ErrMalformedTag) || !errors.Is(rerr, ErrMalformedTag) || terr.Error() != rerr.Error() {
				t.Fatalf("%q: refusals differ: %v / %v", in, terr, rerr)
			}
			continue
		}
		var names []string
		for _, tag := range tags {
			names = append(names, tag.Name)
		}
		if !slices.Equal(tags, asked) || !slices.Equal(names, c.reads) {
			t.Fatalf("%q: names %v, guard asked %v, vault read %v", in, tags, asked, c.reads)
		}
		resolved += len(tags)
		rc.Zero()
	}
	if resolved < 5 {
		t.Fatalf("the corpus resolved only %d tags; the comparison is vacuous", resolved)
	}
}

func TestRehydratorExportsOnlyRehydrateOnly(t *testing.T) {
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes}, ".")
	if err != nil || len(pkgs) != 1 || len(pkgs[0].Errors) != 0 {
		t.Fatalf("load internal/secrets: %v %v", err, pkgs)
	}
	obj := pkgs[0].Types.Scope().Lookup("Rehydrator")
	if obj == nil {
		t.Fatal("no Rehydrator type")
	}
	var exported []string
	for _, typ := range []types.Type{obj.Type(), types.NewPointer(obj.Type())} {
		set := types.NewMethodSet(typ)
		for i := range set.Len() {
			if name := set.At(i).Obj().Name(); token.IsExported(name) && !slices.Contains(exported, name) {
				exported = append(exported, name)
			}
		}
	}
	if !slices.Equal(exported, []string{"RehydrateOnly"}) {
		t.Fatalf("*secrets.Rehydrator exports %v, want exactly [RehydrateOnly]", exported)
	}
}
