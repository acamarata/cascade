package generate

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	endLn   = "<!-- cascade:managed:end -->\n"
	beginB2 = "<!-- cascade:managed:begin b2 -->\n"
)

// injectionCase is a body that tries to leave its block.
type injectionCase struct {
	name, body string
	line       int
}

func injectionCases() []injectionCase {
	return []injectionCase{
		{"end then begin pair", "b\n" + endLn + "INJECTED\n" + beginB2 + "z\n", 2},
		{"lone end", "x\n" + endLn, 2},
		{"indented end", "  \t" + endLn, 1},
		{"lone begin", beginB2, 1},
		{"indented begin", "x\n y\n\t" + beginB2, 3},
		{"malformed begin", "<!-- cascade:managed:begin -->\n", 1},
		{"CRLF end", "x\r\n<!-- cascade:managed:end -->\r\n", 2},
	}
}

// wantMarkerBody asserts err is the marker-body refusal for block id at line.
func wantMarkerBody(t *testing.T, err error, id string, line int) {
	t.Helper()
	msg := "generate: body of block \"" + id + "\" line " + string(rune('0'+line)) + " holds a managed-block marker"
	wantKind(t, err, cascade.KindInvalidInput, msg)
	if !isMangled(err) {
		t.Fatalf("%v does not wrap ErrMangledMarker", err)
	}
}

func TestReplaceManagedBlockRefusesMarkerBodies(t *testing.T) {
	doc := []byte(baseFile())
	for _, tc := range injectionCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReplaceManagedBlock(doc, FormMarkdown, "main", []byte(tc.body))
			wantMarkerBody(t, err, "main", tc.line)
			if got != nil {
				t.Fatalf("content returned with the error: %q", got)
			}
		})
	}
	t.Run("a mention inside a longer line is plain text", func(t *testing.T) {
		body := "see <!-- cascade:managed:end --> in the docs\n"
		got, err := ReplaceManagedBlock(doc, FormMarkdown, "main", []byte(body))
		if err != nil || string(got) != mdFile("main", body, "footer\n") {
			t.Fatalf("got %q %v", got, err)
		}
	})
}

func TestApplyBlockRefusesMarkerBodies(t *testing.T) {
	for _, tc := range injectionCases() {
		for _, mode := range []struct{ name, gen, id string }{{"replace", genA, "main"}, {"append", genB, "extra"}} {
			t.Run(mode.name+" "+tc.name, func(t *testing.T) {
				repo, _ := newRepo(t)
				m := seed(t, repo)
				before := snapshot(t, repo)
				rec := &recorder{}
				w, _ := NewWriter(repo, rec.sink(), m, true)
				out, err := w.ApplyBlock(context.Background(), target, mode.gen, mode.id, []byte(tc.body))
				wantMarkerBody(t, err, mode.id, tc.line)
				if out.Written || out.Adopted || out.Sidecar != "" {
					t.Fatalf("outcome = %+v", out)
				}
				sameTree(t, before, snapshot(t, repo))
				if slurp(t, repo, target) != baseFile() || len(rec.calls) != 0 {
					t.Fatalf("file changed or sink called: %v", rec.calls)
				}
			})
		}
	}
}

func TestVerifyEditRefusesAChangedBlockSet(t *testing.T) {
	before, err := scanBlocks([]byte(baseFile()), FormMarkdown, nil)
	if err != nil {
		t.Fatal(err)
	}
	two := baseFile() + string(WrapBlock(FormMarkdown, "b2", []byte("z\n")))
	for name, c := range map[string]struct {
		out, added string
	}{
		"extra block":    {two, ""},
		"dropped block":  {"# Title\n", ""},
		"renamed block":  {mdFile("other", "one\n", "footer\n"), ""},
		"append missing": {baseFile(), "b2"},
		"wrong id added": {two, "b3"},
	} {
		err := verifyEdit("main", []byte(c.out), FormMarkdown, before, c.added)
		wantKind(t, err, cascade.KindInvalidInput, "generate: edit of block \"main\" changed the set of managed blocks")
		if !isMangled(err) {
			t.Fatalf("%s: not ErrMangledMarker", name)
		}
	}
	if err := verifyEdit("b2", []byte(two), FormMarkdown, before, "b2"); err != nil {
		t.Fatalf("a correct append refused: %v", err)
	}
	if err := verifyEdit("main", []byte(baseFile()), FormMarkdown, before, ""); err != nil {
		t.Fatalf("a correct replace refused: %v", err)
	}
}
