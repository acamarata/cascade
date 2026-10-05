package context

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Exercises the v2 tier names, the legacyTierName helper and the
// legacyNormalize bridge over the frozen harvested goldens (retired labels,
// digests and one retired MCP command line; nothing else changes).

const (
	legacyMCPLine = "**MCP server:** `stdio: cascade mcp stdio`"
	v2MCPLine     = "**MCP server:** `stdio: cascade mcp serve --stdio`"
)

var (
	legacyLabels  = `(ASI|PPI|PRI|PAI)`
	headerLabelRe = regexp.MustCompile(`(?m)^(## Cascade Context — )` + legacyLabels + `( Tier \()`)
	jsonRoleRe    = regexp.MustCompile(`("role": ")` + legacyLabels + `(")`)
	digestRe      = regexp.MustCompile(`digest=sha256:[0-9a-f]{64}`)
)

// legacyTierName resolves a retired tier label (All-Sites, Per-Project,
// Per-Repo, Per-App Instructions) to its v2 role by role, never by walk
// position. Test-only: production code renders and reads no retired label.
func legacyTierName(label string) (TierRole, bool) {
	r, ok := map[string]TierRole{"ASI": TierAPC, "PPI": TierPPC, "PRI": TierPRC, "PAI": TierPAC}[label]
	return r, ok
}

// legacyRole maps a retired label to its v2 label through legacyTierName.
func legacyRole(label string) string {
	r, _ := legacyTierName(label)
	return r.String()
}

func relabel(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		g := re.FindStringSubmatch(m)
		return g[1] + legacyRole(g[2]) + g[3]
	})
}

// legacyNormalize rewrites tier labels (headers and JSON role fields) through legacyTierName, masks content digests, and rewrites
// exactly one whole line: the retired MCP command line.
func legacyNormalize(s string) string {
	if strings.Contains(s, "— ") {
		s = relabel(headerLabelRe, s)
	}
	if strings.Contains(s, `"role": "`) {
		s = relabel(jsonRoleRe, s)
	}
	s = digestRe.ReplaceAllString(s, "digest=sha256:<masked>")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == legacyMCPLine {
			lines[i] = v2MCPLine
		}
	}
	return strings.Join(lines, "\n")
}

// digestConsistent reports whether a rendered managed block's digest
// attribute equals the sha256 of its body, since legacyNormalize masks it.
func digestConsistent(block string) bool {
	line, body, ok := strings.Cut(block, "\n")
	m := digestRe.FindString(line)
	if !ok || m == "" {
		return false
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(body, "\n")))
	return m == "digest=sha256:"+hex.EncodeToString(sum[:])
}

func TestTierNamesV2AndLegacyAliases(t *testing.T) {
	roles := []struct {
		role   TierRole
		v2     string
		legacy string
	}{
		{TierGCI, "GCI", ""}, {TierAPC, "APC", "ASI"}, {TierPPC, "PPC", "PPI"},
		{TierPRC, "PRC", "PRI"}, {TierPAC, "PAC", "PAI"},
	}
	for _, r := range roles {
		if got := r.role.String(); got != r.v2 {
			t.Errorf("%d.String() = %q, want %q", r.role, got, r.v2)
		}
		if r.legacy == "" {
			continue
		}
		got, ok := legacyTierName(r.legacy)
		if !ok || got != r.role {
			t.Errorf("legacyTierName(%q) = %v (ok=%v), want %v by role", r.legacy, got, ok, r.role)
		}
	}
	for _, retired := range []string{"ASI", "PPI", "PRI", "PAI"} {
		for _, r := range allTierRoles() {
			if r.String() == retired {
				t.Errorf("%v still renders the retired label %q", r, retired)
			}
		}
	}
}

func TestLegacyNormalizeOnlyLabelsAndDigests(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	in := "<!-- cascade:generate-instructions digest=sha256:" + digest + " -->\n" +
		"## Cascade Context — PAI Tier (Per-App Instructions)\n\n" + legacyMCPLine + "\n"
	want := "<!-- cascade:generate-instructions digest=sha256:<masked> -->\n" +
		"## Cascade Context — PAC Tier (Per-App Instructions)\n\n" + v2MCPLine + "\n"
	if got := legacyNormalize(in); got != want {
		t.Errorf("legacyNormalize:\n got %q\nwant %q", got, want)
	}
	if got := legacyNormalize(`{"role": "PPI", "x": "PPI"}`); got != `{"role": "PPC", "x": "PPI"}` {
		t.Errorf("JSON role rewrite touched more than the role field: %q", got)
	}
	untouched := []string{
		"# Per-Repo Instructions (PRI)\n", "body PRI text ASI\n", "digest=sha256:beef\n",
		"stdio: cascade mcp stdio\n", "**MCP server:** `stdio: cascade mcp stdio` \n",
		"x**MCP server:** `stdio: cascade mcp stdio`\n",
	}
	for _, u := range untouched {
		if got := legacyNormalize(u); got != u {
			t.Errorf("legacyNormalize altered %q into %q", u, got)
		}
	}
}

// singleByteCase is one golden/generator pair for the acceptance-[9] proof.
type singleByteCase struct {
	name   string
	golden string
	// matches reports whether golden passes the comparison against the
	// generator output the case was built from.
	matches func(golden string) bool
	// scope limits the mutated byte range to [lo,hi) spans (nil = whole file).
	scope [][2]int
}

func TestLegacyNormalizeMCPRuleOnly(t *testing.T) {
	cases := append(roundtripCases(t), v1CCCases(t)...)
	if len(cases) != 3+5 {
		t.Fatalf("built %d single-byte cases, want 8", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(c.golden, legacyMCPLine+"\n") {
				t.Fatalf("golden lacks the retired MCP line; the case proves nothing")
			}
			if !c.matches(c.golden) {
				t.Fatal("the unmodified golden does not match the generator output")
			}
			near := strings.Replace(c.golden, legacyMCPLine+"\n", "**MCP server:** `stdio: cascade mcp stdiox`\n", 1)
			if c.matches(near) {
				t.Error("a golden with `stdiox` in the MCP line still matches")
			}
			assertEverySingleByteFails(t, c)
		})
	}
}

// assertEverySingleByteFails mutates each byte of the golden that is not
// part of a tier label or a digest and requires the comparison to fail.
func assertEverySingleByteFails(t *testing.T, c singleByteCase) {
	t.Helper()
	skip := map[int]bool{}
	for _, re := range []*regexp.Regexp{headerLabelRe, digestRe} {
		for _, m := range re.FindAllStringSubmatchIndex(c.golden, -1) {
			lo, hi := m[0], m[1]
			if re == headerLabelRe {
				lo, hi = m[4], m[5]
			}
			for i := lo; i < hi; i++ {
				skip[i] = true
			}
		}
	}
	b := []byte(c.golden)
	tested := 0
	for _, sp := range spansOrWhole(c) {
		for i := sp[0]; i < sp[1]; i++ {
			if skip[i] {
				continue
			}
			orig := b[i]
			b[i] = orig ^ 0x01
			if c.matches(string(b)) {
				t.Errorf("mutating byte %d (%q) still matched: the normalizer hides it", i, orig)
			}
			b[i] = orig
			tested++
		}
	}
	if tested < 100 {
		t.Errorf("only %d bytes mutated; the proof is vacuous", tested)
	}
}

func spansOrWhole(c singleByteCase) [][2]int {
	if c.scope != nil {
		return c.scope
	}
	return [][2]int{{0, len(c.golden)}}
}

func roundtripCases(t *testing.T) []singleByteCase {
	t.Helper()
	mc := roundtripMerged(t)
	gen := func(w HarnessGenerator) map[TierRole]string {
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		out := map[TierRole]string{}
		for _, f := range files {
			out[f.Role] = string(f.Content)
			assertFreshBlock(t, string(f.Content))
		}
		return out
	}
	cc, oc, cx := gen(&CCInstructionWriter{}), gen(&OCInstructionWriter{}), gen(&CXInstructionWriter{})
	whole := func(name string, ref string, golden string) singleByteCase {
		nref := legacyNormalize(ref)
		return singleByteCase{name: name, golden: golden, matches: func(g string) bool {
			return legacyNormalize(g) == nref
		}}
	}
	env := readReference(t, "codex", "AGENTS.md")
	blocks := []string{legacyNormalize(cx[TierGCI]), legacyNormalize(cx[TierPRC]), legacyNormalize(cx[TierPAC])}
	return []singleByteCase{
		whole("roundtrip/claude", cc[TierPRC], readReference(t, "claude", "CLAUDE.md")),
		whole("roundtrip/opencode", oc[TierPRC], readReference(t, "opencode", "AGENTS.md")),
		{name: "roundtrip/codex", golden: env, scope: blockSpans(env), matches: func(g string) bool {
			ng := legacyNormalize(g)
			for _, blk := range blocks {
				if !strings.Contains(ng, blk) {
					return false
				}
			}
			return true
		}},
	}
}

// blockSpans returns the byte spans of every managed block in s.
func blockSpans(s string) [][2]int {
	const open, closer = "<!-- cascade:generate-instructions", "<!-- /cascade:generate-instructions -->"
	var spans [][2]int
	for at := 0; ; {
		i := strings.Index(s[at:], open)
		if i < 0 {
			return spans
		}
		j := strings.Index(s[at+i:], closer)
		if j < 0 {
			return spans
		}
		spans = append(spans, [2]int{at + i, at + i + j + len(closer)})
		at += i + j + len(closer)
	}
}

func v1CCCases(t *testing.T) []singleByteCase {
	t.Helper()
	files, err := (&CCInstructionWriter{}).Generate(mergeGoldenCorpus(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var out []singleByteCase
	for _, f := range files {
		name, ref := ccGoldenFiles[f.Role], string(f.Content)
		assertFreshBlock(t, ref)
		nref := legacyNormalize(ref)
		out = append(out, singleByteCase{
			name: filepath.Join("v1-goldens/cc", name), golden: loadCCGolden(t, name),
			matches: func(g string) bool { return legacyNormalize(g) == nref },
		})
	}
	return out
}

// assertFreshBlock requires generator output to be self-consistent and free
// of the retired command: the normalizer never gets to hide either.
func assertFreshBlock(t *testing.T, block string) {
	t.Helper()
	if strings.Contains(block, "cascade mcp stdio\n") || strings.Contains(block, "cascade mcp stdio`") {
		t.Error("generator output carries the retired `cascade mcp stdio` command")
	}
	if !strings.Contains(block, v2MCPLine+"\n") {
		t.Error("generator output lacks the v2 MCP line")
	}
	if !digestConsistent(block) {
		t.Error("generator output digest does not match its body")
	}
}
