// Purpose: SnapshotVault reads a vault once and offers the JSON-escaped forms
//
//	of its values, byte-for-byte as encoding/json writes them.
//
// SPORT: AUDIT_REDACTION: ADD (tests, internal/secrets.SnapshotVault).
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// listGetSource is a fixed value source that counts its reads.
type listGetSource struct {
	values  map[string]string
	listErr error
	getErr  error
	lists   int
	gets    int
}

func (s *listGetSource) List(context.Context) ([]string, error) {
	s.lists++
	if s.listErr != nil {
		return nil, s.listErr
	}
	names := make([]string, 0, len(s.values))
	for name := range s.values {
		names = append(names, name)
	}
	return names, nil
}

func (s *listGetSource) Get(_ context.Context, name string) ([]byte, error) {
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	return []byte(s.values[name]), nil
}

func TestSnapshotVaultReadsSourceOnce(t *testing.T) {
	ctx := context.Background()
	src := &listGetSource{values: map[string]string{"b-key": "second-value-88", "a-key": "first-value-1234"}}
	snap, err := NewSnapshotVault(ctx, src)
	if err != nil {
		t.Fatalf("NewSnapshotVault: %v", err)
	}
	for i := 0; i < 3; i++ {
		names, _ := snap.List(ctx)
		if strings.Join(names, ",") != "a-key,b-key" {
			t.Fatalf("List = %v, want sorted names", names)
		}
		if got, err := snap.Get(ctx, "a-key"); err != nil || string(got) != "first-value-1234" {
			t.Fatalf("Get(a-key) = %q, %v", got, err)
		}
	}
	if _, err := snap.Get(ctx, "absent"); err == nil {
		t.Fatal("Get of an absent name returned no error")
	}
	if src.lists != 1 || src.gets != 2 {
		t.Fatalf("source read %d lists and %d gets, want 1 and 2", src.lists, src.gets)
	}
}

func TestSnapshotVaultFailsClosed(t *testing.T) {
	boom := errors.New("custody unavailable")
	for name, src := range map[string]*listGetSource{
		"list fails": {values: map[string]string{"k": "some-value-1234"}, listErr: boom},
		"get fails":  {values: map[string]string{"k": "some-value-1234"}, getErr: boom},
	} {
		if snap, err := NewSnapshotVault(context.Background(), src); err == nil || snap != nil {
			t.Errorf("%s: NewSnapshotVault = (%v, %v), want (nil, error)", name, snap, err)
		}
	}
}

// jsonForms returns the inside of the JSON string literals encoding/json
// writes for s, with and without HTML escaping.
func jsonForms(t *testing.T, s string) (html, plain string) {
	t.Helper()
	htmlBytes, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	plainBytes := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return string(htmlBytes[1 : len(htmlBytes)-1]), string(plainBytes[1 : len(plainBytes)-1])
}

// allForms returns every JSON-escaped form of raw up to depth levels, the
// way encoding/json writes them with HTML escaping on and off, computed
// independently of SnapshotVault (excluding raw itself).
func allForms(t *testing.T, raw string, depth int) map[string]bool {
	t.Helper()
	out, level := map[string]bool{}, []string{raw}
	for d := 0; d < depth; d++ {
		var next []string
		for _, f := range level {
			html, plain := jsonForms(t, f)
			for _, g := range []string{html, plain} {
				if g != f {
					out[g] = true
					next = append(next, g)
				}
			}
		}
		level = next
	}
	return out
}

// snapshotForms returns every value held by the snapshots for name.
func snapshotForms(forms []*SnapshotVault, name string) map[string]bool {
	out := map[string]bool{}
	for _, f := range forms {
		if got, err := f.Get(context.Background(), name); err == nil {
			out[string(got)] = true
		}
	}
	return out
}

func TestSnapshotVaultJSONFormsMatchEncodingJSON(t *testing.T) {
	tricky := map[string]string{
		"quote":     `pass"word-1234`,
		"backslash": `pa\ss\word-1234`,
		"angle":     `pass<word>-1234`,
		"amp":       `pass&word-12345`,
		"mixed":     `pa"ss<w>-1234`, // HTML escaping on and off differ after the quote
		"newline":   "pass\nword-12345",
		"line-sep":  "pass\u2028word-1234",
		"eight":     `ab"cd<ef`, // exactly the floor
	}
	snap, err := NewSnapshotVault(context.Background(), &listGetSource{values: tricky})
	if err != nil {
		t.Fatal(err)
	}
	forms := snap.JSONForms(3)
	for name, raw := range tricky {
		want, got := allForms(t, raw, 3), snapshotForms(forms, name)
		for f := range want {
			if !got[f] {
				t.Errorf("%s: form %q missing", name, f)
			}
		}
		for f := range got {
			if !want[f] {
				t.Errorf("%s: unexpected form %q", name, f)
			}
		}
	}
	if got := len(snap.JSONForms(0)); got != 0 {
		t.Fatalf("JSONForms(0) = %d snapshots, want none", got)
	}
}

func TestSnapshotVaultJSONFormsSkipPlainAndShortValues(t *testing.T) {
	snap, err := NewSnapshotVault(context.Background(), &listGetSource{values: map[string]string{
		"plain": "plainvalue-12345", "short": `a"b<c>`, "quote": `pass"word-1234`}})
	if err != nil {
		t.Fatal(err)
	}
	forms := snap.JSONForms(3)
	for _, name := range []string{"plain", "short"} {
		if got := snapshotForms(forms, name); len(got) != 0 {
			t.Errorf("%s: got escaped forms %v for a value that needs none or is under the floor", name, got)
		}
	}
	// A lone value with no HTML characters has exactly one form per level,
	// deepest first.
	lone, err := NewSnapshotVault(context.Background(), &listGetSource{values: map[string]string{"quote": `pass"word-1234`}})
	if err != nil {
		t.Fatal(err)
	}
	var lens []int
	for _, f := range lone.JSONForms(3) {
		got, _ := f.Get(context.Background(), "quote")
		lens = append(lens, len(got))
	}
	if len(lens) != 3 || lens[0] <= lens[1] || lens[1] <= lens[2] {
		t.Fatalf("form lengths = %v, want three, longest first", lens)
	}
}

func TestSnapshotVaultExactHits(t *testing.T) {
	ctx := context.Background()
	snap, err := NewSnapshotVault(ctx, &listGetSource{values: map[string]string{
		"canary-token": `pass"word-1234`, "tiny": "short"}})
	if err != nil {
		t.Fatal(err)
	}
	html, _ := jsonForms(t, `pass"word-1234`)
	text := []byte(`raw pass"word-1234, escaped ` + html + `, again ` + html + `, tiny short`)
	hits := snap.ExactHits(text, 3)
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3 (one raw, two escaped; the 5-byte value is under the floor): %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.SuggestedName != "CANARY_TOKEN" || h.Class != ClassAPIKey || h.Confidence != ConfidenceProven {
			t.Errorf("hit %+v is not labelled like the egress exact pass", h)
		}
		if h.Len == 0 || h.Offset+h.Len > len(text) {
			t.Errorf("hit %+v lies outside the text", h)
		}
	}
	res, err := NewRewriter().Rewrite(text, hits)
	if err != nil || bytes.Contains(res.Text, []byte("word-1234")) {
		t.Fatalf("rewrite = %q, %v; the value survived", res.Text, err)
	}
}
