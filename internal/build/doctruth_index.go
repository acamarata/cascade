package build

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// docIndexSpec describes one "every page under dir must be linked from its
// index page(s)" rule instance.
type docIndexSpec struct {
	primary string          // the index page that hosts the missing-page finding
	altSrc  string          // an alternate page whose links also count (wiki's _Sidebar.md); "" if none
	dir     string          // directory whose tracked *.md pages must all be linked
	exclude map[string]bool // basenames under dir excluded from the "must be linked" set
}

var docIndexSpecs = []docIndexSpec{
	{
		primary: ".github/wiki/Home.md",
		altSrc:  ".github/wiki/_Sidebar.md",
		dir:     ".github/wiki",
		exclude: map[string]bool{"Home.md": true, "_Sidebar.md": true, "_Footer.md": true},
	},
	{primary: "docs/security-posture.md", dir: "docs/security-posture", exclude: map[string]bool{}},
	{primary: "docs/quickstart/README.md", dir: "docs/quickstart", exclude: map[string]bool{"README.md": true}},
}

// checkIndexes runs every applicable docIndexSpec. applies reports whether
// a given index page is in scope for this run (whole-scope run, or the
// page is an expanded argument or lies under a directory argument).
func checkIndexes(repoRoot string, tracked map[string]bool, applies func(indexPage string) bool, k *keyer) ([]DocFinding, error) {
	var findings []DocFinding
	for _, spec := range docIndexSpecs {
		if !applies(spec.primary) {
			continue
		}
		f, err := checkOneIndex(repoRoot, tracked, spec, k)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	return findings, nil
}

func checkOneIndex(repoRoot string, tracked map[string]bool, spec docIndexSpec, k *keyer) ([]DocFinding, error) {
	if !tracked[spec.primary] {
		return []DocFinding{mkFinding(k, spec.primary, 0, DocRuleIndex, "missing index page: "+spec.primary, spec.primary)}, nil
	}
	linked, err := indexedTargets(repoRoot, tracked, spec)
	if err != nil {
		return nil, err
	}
	var findings []DocFinding
	for page := range tracked {
		if !strings.HasPrefix(page, spec.dir+"/") || !strings.HasSuffix(page, ".md") {
			continue
		}
		if strings.Contains(strings.TrimPrefix(page, spec.dir+"/"), "/") {
			continue // only direct children, matching "docs/quickstart/*.md" etc.
		}
		base := page[strings.LastIndex(page, "/")+1:]
		if spec.exclude[base] {
			continue
		}
		if !linked[page] {
			findings = append(findings, mkFinding(k, spec.primary, 0, DocRuleIndex, "unlinked page: "+page, page))
		}
	}
	return findings, nil
}

func indexedTargets(repoRoot string, tracked map[string]bool, spec docIndexSpec) (map[string]bool, error) {
	out := map[string]bool{}
	for _, src := range []string{spec.primary, spec.altSrc} {
		if src == "" || !tracked[src] {
			continue
		}
		data, err := readFileFn(pathJoin(repoRoot, src))
		if err != nil {
			return nil, wrapReadErr("doctruth: reading index page", src, err)
		}
		for _, l := range extractLinks(string(data)) {
			target, external := mapLinkTarget(src, l.Target)
			if !external {
				out[target] = true
			}
		}
	}
	return out, nil
}

// trackedSets returns the whole repo's tracked file set and the set of
// every directory that contains a tracked file (a link may point at a
// directory, e.g. a tree/ URL).
func trackedSets(repoRoot string) (files, dirs map[string]bool, err error) {
	all, err := gitLsFilesFn(repoRoot)
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "doctruth: listing tracked files")
	}
	files = map[string]bool{}
	dirs = map[string]bool{}
	for _, f := range all {
		files[f] = true
		d := f
		for {
			idx := strings.LastIndexByte(d, '/')
			if idx < 0 {
				break
			}
			d = d[:idx]
			dirs[d] = true
		}
	}
	return files, dirs, nil
}

// expandArgs turns files (repo-relative, possibly directories) into the
// sorted set of in-scope tracked *.md files they name, plus the set of
// arguments that were directories (used by the index rule's "lies under a
// directory argument" test). files empty means "the whole scope".
func expandArgs(scope []string, files []string) (targets []string, dirArgs map[string]bool, err error) {
	dirArgs = map[string]bool{}
	if len(files) == 0 {
		return scope, dirArgs, nil
	}
	scopeSet := map[string]bool{}
	for _, f := range scope {
		scopeSet[f] = true
	}
	seen := map[string]bool{}
	for _, arg := range files {
		arg = strings.TrimSuffix(filepath.ToSlash(arg), "/")
		if scopeSet[arg] {
			if !seen[arg] {
				seen[arg] = true
				targets = append(targets, arg)
			}
			continue
		}
		matched := false
		prefix := arg + "/"
		for _, f := range scope {
			if strings.HasPrefix(f, prefix) {
				matched = true
				if !seen[f] {
					seen[f] = true
					targets = append(targets, f)
				}
			}
		}
		if !matched {
			return nil, nil, cascade.Newf(cascade.KindNotFound, "doctruth: %s is not a tracked in-scope file or directory", arg)
		}
		dirArgs[arg] = true
	}
	sort.Strings(targets)
	return targets, dirArgs, nil
}
