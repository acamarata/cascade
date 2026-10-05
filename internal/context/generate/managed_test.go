package generate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// formCase is one marker form with a file it selects.
type formCase struct {
	name, path string
	form       MarkerForm
	begin, end string
}

var formCases = []formCase{
	{"markdown", "AGENTS.md", FormMarkdown, "<!-- cascade:managed:begin %s -->", "<!-- cascade:managed:end -->"},
	{"toml", ".codex/config.toml", FormHash, "# cascade:managed:begin %s", "# cascade:managed:end"},
	{"slash", "opencode.json", FormSlash, "// cascade:managed:begin %s", "// cascade:managed:end"},
}

// doc builds a file with hand text around two blocks in form fc.
func (fc formCase) doc() string {
	b := func(id, body string) string {
		return strings.Replace(fc.begin, "%s", id, 1) + "\n" + body + fc.end + "\n"
	}
	return "hand top\n" + b("one", "alpha\n") + "hand middle\n" + b("two", "beta\n") + "hand bottom\n"
}

// checkFormParse asserts parse, replace, wrap and duplicate-id behaviour of
// one marker form.
func checkFormParse(t *testing.T, fc formCase) {
	t.Helper()
	if f, err := FormFor(fc.path); err != nil || f != fc.form {
		t.Fatalf("FormFor(%s) = %v %v", fc.path, f, err)
	}
	doc := fc.doc()
	blocks, err := ParseManagedBlocks([]byte(doc), fc.form, nil)
	if err != nil || len(blocks) != 2 || blocks[0].ID != "one" || string(blocks[0].Body) != "alpha\n" ||
		blocks[1].ID != "two" || string(blocks[1].Body) != "beta\n" {
		t.Fatalf("blocks = %+v %v", blocks, err)
	}
	got, err := ReplaceManagedBlock([]byte(doc), fc.form, "two", []byte("gamma\ndelta\n"))
	if err != nil || string(got) != strings.Replace(doc, "beta\n", "gamma\ndelta\n", 1) {
		t.Fatalf("replace changed more than the body: %v\n%s", err, got)
	}
	if wrapped := WrapBlock(fc.form, "x", []byte("y")); string(wrapped) != strings.Replace(fc.begin, "%s", "x", 1)+"\ny\n"+fc.end+"\n" {
		t.Fatalf("WrapBlock = %q", wrapped)
	}
	dup := doc + strings.Replace(fc.begin, "%s", "one", 1) + "\n" + fc.end + "\n"
	if _, err := ParseManagedBlocks([]byte(dup), fc.form, nil); err == nil || !isMangled(err) || !strings.Contains(err.Error(), "duplicate block id one") {
		t.Fatalf("duplicate id accepted: %v", err)
	}
}

func TestManagedBlockForms(t *testing.T) {
	for _, fc := range formCases {
		t.Run(fc.name, func(t *testing.T) {
			checkFormParse(t, fc)
			if _, err := ReplaceManagedBlock([]byte(fc.doc()), fc.form, "nope", nil); err == nil || !strings.Contains(err.Error(), "not-found") {
				t.Fatalf("absent id: %v", err)
			}
			if _, err := ReplaceManagedBlock([]byte(fc.doc()), fc.form, "one", []byte(fc.end+"\n")); err == nil {
				t.Fatal("a body carrying an end marker was accepted")
			}
		})
	}
}

func TestManagedBlockMangled(t *testing.T) {
	fc := formCases[0]
	begin := func(id string) string { return strings.Replace(fc.begin, "%s", id, 1) + "\n" }
	end := fc.end + "\n"
	cases := map[string]struct {
		doc   string
		known map[string]bool
		line  string
	}{
		"unmatched begin": {"x\n" + begin("a") + "x\n", nil, "line 2"},
		"unmatched end":   {"x\n" + end, nil, "line 2"},
		"nested begin":    {begin("a") + begin("b") + end + end, nil, "line 2"},
		"duplicate id":    {begin("a") + end + begin("a") + end, nil, "line 3"},
		"unknown id":      {begin("a") + end + begin("b") + end, map[string]bool{"a": true}, "line 3"},
		"bad id":          {begin("Bad_ID") + end, nil, "line 1"},
		"empty id":        {"<!-- cascade:managed:begin  -->\n" + end, nil, "line 1"},
		"trailing space":  {begin("a")[:len(begin("a"))-1] + " \n" + end, nil, "line 1"},
		"missing suffix":  {"<!-- cascade:managed:begin a\n" + end, nil, "line 1"},
		"trailing text":   {begin("a")[:len(begin("a"))-1] + " x\n" + end, nil, "line 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManagedBlocks([]byte(c.doc), FormMarkdown, c.known)
			if err == nil || !isMangled(err) || !strings.Contains(err.Error(), c.line) {
				t.Fatalf("err = %v, want mangled at %s", err, c.line)
			}
			if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
				t.Fatalf("kind = %v", k)
			}
		})
	}
}

func TestManagedBlockPlainText(t *testing.T) {
	t.Run("mentions are text", func(t *testing.T) {
		doc := "use `<!-- cascade:managed:begin x -->` to open a block\n" + "see <!-- cascade:managed:end --> too\n"
		blocks, err := ParseManagedBlocks([]byte(doc), FormMarkdown, nil)
		if err != nil || len(blocks) != 0 {
			t.Fatalf("blocks=%v err=%v", blocks, err)
		}
	})
	t.Run("crlf markers", func(t *testing.T) {
		doc := "<!-- cascade:managed:begin a -->\r\nbody\r\n<!-- cascade:managed:end -->\r\n"
		blocks, err := ParseManagedBlocks([]byte(doc), FormMarkdown, nil)
		if err != nil || len(blocks) != 1 || string(blocks[0].Body) != "body\r\n" {
			t.Fatalf("blocks=%+v err=%v", blocks, err)
		}
	})
	t.Run("unsupported extension", func(t *testing.T) {
		for _, p := range []string{"notes.txt", "config.yaml", "Makefile", "x.md.bak"} {
			_, err := FormFor(p)
			wantKind(t, err, cascade.KindInvalidInput, "generate: no marker form for \""+p+"\"")
		}
	})
}

func TestManagedBlockJSONCForm(t *testing.T) {
	doc := "{\n  // cascade:managed:begin mcp\n  \"mcp\": {},\n  // cascade:managed:end\n  \"hand\": true\n}\n"
	form, err := FormFor("opencode.jsonc")
	if err != nil || form != FormSlash {
		t.Fatalf("FormFor = %v %v", form, err)
	}
	got, err := ReplaceManagedBlock([]byte(doc), form, "mcp", []byte("  \"mcp\": {\"a\": 1},\n"))
	if err != nil || !strings.Contains(string(got), "\"a\": 1") || !strings.HasSuffix(string(got), "  \"hand\": true\n}\n") ||
		!strings.HasPrefix(string(got), "{\n  // cascade:managed:begin mcp\n") {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestManagedBlockJSForm(t *testing.T) {
	doc := "export const a = 1;\n// cascade:managed:begin wf\nexport const wf = [];\n// cascade:managed:end\nexport const b = 2;\n"
	form, err := FormFor("workflows/plan.js")
	if err != nil || form != FormSlash {
		t.Fatalf("FormFor = %v %v", form, err)
	}
	got, err := ReplaceManagedBlock([]byte(doc), form, "wf", []byte("export const wf = [1];\n"))
	want := "export const a = 1;\n// cascade:managed:begin wf\nexport const wf = [1];\n// cascade:managed:end\nexport const b = 2;\n"
	if err != nil || string(got) != want {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestWrapBlockRoundTrip(t *testing.T) {
	for _, fc := range formCases {
		for _, body := range []string{"", "one\n", "a\nb\n"} {
			wrapped := WrapBlock(fc.form, "id", []byte(body))
			blocks, err := ParseManagedBlocks(wrapped, fc.form, map[string]bool{"id": true})
			if err != nil || len(blocks) != 1 || string(blocks[0].Body) != body {
				t.Fatalf("%s %q: %+v %v", fc.name, body, blocks, err)
			}
		}
	}
	if got := WrapBlock(FormHash, "id", []byte("no lf")); !bytes.Contains(got, []byte("\nno lf\n# cascade:managed:end")) {
		t.Fatalf("missing LF not added: %q", got)
	}
}

func FuzzParseManagedBlocks(f *testing.F) {
	for _, fc := range formCases {
		f.Add([]byte(fc.doc()), uint8(fc.form))
	}
	f.Add([]byte("<!-- cascade:managed:begin a -->\n<!-- cascade:managed:begin b -->\n"), uint8(1))
	f.Add([]byte("# cascade:managed:end\n"), uint8(2))
	f.Add([]byte(""), uint8(3))
	f.Fuzz(func(t *testing.T, content []byte, f uint8) {
		form := MarkerForm(f%3) + 1
		blocks, err := ParseManagedBlocks(content, form, nil)
		if err != nil {
			if !isMangled(err) {
				t.Fatalf("error is not a mangled-marker error: %v", err)
			}
			return
		}
		for _, b := range blocks {
			got, err := ReplaceManagedBlock(content, form, b.ID, b.Body)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("replacing %q with its own body changed the file: %v", b.ID, err)
			}
		}
	})
}
