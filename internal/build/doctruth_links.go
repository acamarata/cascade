package build

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// DocLink is one markdown link found on a page, inline or reference-style.
type DocLink struct {
	Line     int
	Raw      string
	Target   string // repo-relative; empty for a same-page "#anchor" link
	Anchor   string // without the leading '#'
	External bool
}

var (
	inlineLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	refLinkRe    = regexp.MustCompile(`\[([^\]]*)\]\[([^\]]*)\]`)
	refDefRe     = regexp.MustCompile(`^\s{0,3}\[([^\]]+)\]:\s*(\S+)`)

	repoBlobTreeRe = regexp.MustCompile(`^https://github\.com/acamarata/cascade/(blob|tree)/[^/]+/(.*)$`)
	repoWikiRe     = regexp.MustCompile(`^https://github\.com/acamarata/cascade/wiki/([^/#]+)$`)
)

// ResolveDocLinks returns every link found on page (repo-relative,
// forward-slash) plus any findings its own resolution produces standalone
// (this is the entry point TestResolveDocLinks_RepoAndWikiURLs drives; the
// gate's own scan uses checkLinksAndAnchors, which shares mapLinkTarget).
func ResolveDocLinks(repoRoot, page string) ([]DocLink, []DocFinding, error) {
	data, err := readFileFn(pathJoin(repoRoot, page))
	if err != nil {
		return nil, nil, wrapReadErr("doctruth: reading link source", page, err)
	}
	links := extractLinks(string(data))
	for i := range links {
		links[i].Target, links[i].External = mapLinkTarget(page, links[i].Target)
	}
	return links, nil, nil
}

// extractLinks parses inline and reference-style links out of content,
// outside fenced code blocks, splitting each raw target into Target/Anchor
// on the first '#'.
func extractLinks(content string) []DocLink {
	lines := parseDocLines(content)
	defs := map[string]string{}
	for _, dl := range lines {
		if dl.fenced {
			continue
		}
		if m := refDefRe.FindStringSubmatch(dl.text); m != nil {
			defs[strings.ToLower(m[1])] = m[2]
		}
	}
	var out []DocLink
	for i, dl := range lines {
		if dl.fenced {
			continue
		}
		stripped, _ := stripInlineCode(dl.text)
		for _, m := range inlineLinkRe.FindAllStringSubmatch(stripped, -1) {
			out = append(out, splitLink(i+1, m[0], m[2]))
		}
		for _, m := range refLinkRe.FindAllStringSubmatch(stripped, -1) {
			label := m[2]
			if label == "" {
				label = m[1]
			}
			if url, ok := defs[strings.ToLower(label)]; ok {
				out = append(out, splitLink(i+1, m[0], url))
			}
		}
	}
	return out
}

func splitLink(line int, raw, target string) DocLink {
	t, a := target, ""
	if idx := strings.IndexByte(target, '#'); idx >= 0 {
		t, a = target[:idx], target[idx+1:]
	}
	return DocLink{Line: line, Raw: raw, Target: t, Anchor: a}
}

// mapLinkTarget resolves a raw (pre-split) target relative to page into a
// repo-relative path (forward-slash) and reports whether it is external
// (any host other than this repo's github.com URLs — never fetched).
func mapLinkTarget(page, target string) (string, bool) {
	if target == "" {
		return page, false // same-page anchor
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		if m := repoBlobTreeRe.FindStringSubmatch(target); m != nil {
			return path.Clean(m[2]), false
		}
		if m := repoWikiRe.FindStringSubmatch(target); m != nil {
			return ".github/wiki/" + m[1] + ".md", false
		}
		return target, true
	}
	if strings.HasPrefix(target, "mailto:") {
		return target, true
	}
	resolved := path.Clean(path.Join(path.Dir(page), target))
	if strings.HasPrefix(page, ".github/wiki/") && !strings.Contains(path.Base(resolved), ".") {
		// A GitHub wiki page link may omit the .md extension (bare page
		// name); GitHub itself resolves it that way.
		resolved += ".md"
	}
	return resolved, false
}

// headingCache memoizes parseHeadings(page content) per repo-relative
// page path, shared across every page's link/anchor checks in one run.
type headingCache struct {
	repoRoot string
	cache    map[string][]heading
}

func newHeadingCache(repoRoot string) *headingCache {
	return &headingCache{repoRoot: repoRoot, cache: map[string][]heading{}}
}

func (h *headingCache) get(target string) ([]heading, error) {
	if hs, ok := h.cache[target]; ok {
		return hs, nil
	}
	data, err := readFileFn(pathJoin(h.repoRoot, target))
	if err != nil {
		return nil, err
	}
	hs := parseHeadings(string(data))
	h.cache[target] = hs
	return hs, nil
}

// checkLinksAndAnchors runs the link and anchor rules over one page's
// content. tracked/dirs are the whole repo's tracked-file and
// tracked-directory sets (a link may point anywhere in the tree, not just
// the doctruth scope).
func checkLinksAndAnchors(page, content string, tracked, dirs map[string]bool, hc *headingCache, k *keyer) ([]DocFinding, int, error) {
	links := extractLinks(content)
	var findings []DocFinding
	for _, l := range links {
		target, external := mapLinkTarget(page, l.Target)
		l.Target, l.External = target, external
		if external {
			continue
		}
		if !tracked[target] && !dirs[target] {
			findings = append(findings, mkFinding(k, page, l.Line, DocRuleLink,
				"link target not tracked: "+target, l.Raw))
			continue
		}
		if l.Anchor == "" || !tracked[target] || !strings.HasSuffix(target, ".md") {
			continue
		}
		hs, err := hc.get(target)
		if err != nil {
			return nil, 0, wrapReadErr("doctruth: reading anchor target", target, err)
		}
		if !anchorExists(hs, l.Anchor) {
			findings = append(findings, mkFinding(k, page, l.Line, DocRuleAnchor,
				"anchor #"+l.Anchor+" not found in "+target, l.Raw))
		}
	}
	return findings, len(links), nil
}

func anchorExists(hs []heading, anchor string) bool {
	for _, h := range hs {
		if h.slug == anchor {
			return true
		}
	}
	return false
}

func mkFinding(k *keyer, file string, line int, rule DocRule, detail, lineText string) DocFinding {
	return DocFinding{File: file, Line: line, Rule: rule, Detail: detail, Key: k.key(file, rule, lineText)}
}

func pathJoin(root, rel string) string {
	if root == "" {
		return filepath.FromSlash(rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}
