// Package build (this file) holds the test-only gate's falsifiability
// check: does an allow-list entry's promised caller_site actually call the
// symbol it exempts, once that file exists.
//
// LoadTestOnlyAllowList and FilterTestOnlyAllowed (testonlygate.go) already
// catch a stale exemption once the SYMBOL becomes used or is renamed. What
// neither one catches is the entry that names a caller in prose and is
// never checked against it: the file the reason names can be built, ship,
// and still never call the symbol, and the gate stays green because the
// symbol is still, correctly, test-only. CheckCallerSitesWired closes that
// gap by making the promise self-retiring: the day caller_site's file
// exists in the tree, it must already reference the symbol, or the
// exemption is a broken promise rather than a future one.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BrokenCallerSite is one allow-list entry whose caller_site file exists
// but does not reference the exempted symbol.
type BrokenCallerSite struct {
	Symbol     string
	CallerSite string
}

// CheckCallerSitesWired evaluates every entry in allow against the tree
// rooted at moduleRoot. An entry whose caller_site does not exist yet is
// left alone: the work it names is genuinely still future, which is not a
// defect. An entry whose caller_site exists and does not reference its
// symbol is reported.
//
// The only filesystem cost beyond the one os.Stat per entry is, for each
// entry whose file exists, parsing that one file plus the small set of
// files in the symbol's own declaring package (needed only to detect a
// same-package bare-identifier reference; a cross-package call is detected
// through its qualified selector without that scan). Both reuse the exact
// declared/used collectors the rest of this gate already computes with.
func CheckCallerSitesWired(moduleRoot, modulePath string, allow map[string]TestOnlyAllowEntry) ([]BrokenCallerSite, error) {
	var broken []BrokenCallerSite
	for key, e := range allow {
		full := filepath.Join(moduleRoot, e.CallerSite)
		info, statErr := os.Stat(full)
		if statErr != nil || info.IsDir() {
			continue
		}
		wired, err := callerSiteReferencesSymbol(moduleRoot, modulePath, full, e.Symbol)
		if err != nil {
			return nil, err
		}
		if !wired {
			broken = append(broken, BrokenCallerSite{Symbol: key, CallerSite: e.CallerSite})
		}
	}
	sort.Slice(broken, func(i, j int) bool { return broken[i].Symbol < broken[j].Symbol })
	return broken, nil
}

// callerSiteReferencesSymbol reports whether the .go file at callerPath
// references symbol ("<dir>.<Name>"), reusing deadcodeCollectDeclared and
// deadcodeCollectUsed rather than a bespoke scan.
func callerSiteReferencesSymbol(moduleRoot, modulePath, callerPath, symbol string) (bool, error) {
	dir, name, ok := splitSymbolKey(symbol)
	if !ok {
		return false, nil
	}

	declPos, err := declPosForDir(moduleRoot, dir)
	if err != nil {
		return false, err
	}

	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, callerPath, nil, parser.ParseComments)
	if perr != nil {
		return false, perr
	}
	relCaller, relErr := filepath.Rel(moduleRoot, callerPath)
	if relErr != nil {
		return false, relErr
	}
	used, uerr := deadcodeCollectUsed(moduleRoot, modulePath,
		map[string]*ast.File{filepath.Join(moduleRoot, filepath.ToSlash(relCaller)): f}, declPos)
	if uerr != nil {
		return false, uerr
	}
	return used[DeadCodeSymbol{Dir: dir, Name: name}], nil
}

// declPosForDir returns the declaration-position map for symbols declared
// directly in dir (module-relative), so the same-package bare-identifier
// branch of deadcodeCollectUsed can recognize a reference to the symbol
// this entry exempts. A cross-package reference is detected through its
// qualified selector regardless of this map's contents.
func declPosForDir(moduleRoot, dir string) (map[DeadCodeSymbol]token.Pos, error) {
	files, err := deadcodeCollectFiles(moduleRoot, []string{dir})
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	parsed := make(map[string]*ast.File, len(files))
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil, perr
		}
		parsed[path] = f
	}
	_, declPos, err := deadcodeCollectDeclared(moduleRoot, fset, parsed)
	if err != nil {
		return nil, err
	}
	return declPos, nil
}

// splitSymbolKey splits "<dir>.<Name>" into its directory and exported
// name. A symbol's Name is always a single Go identifier (no dots), so the
// last '.' in the key is always the separator.
func splitSymbolKey(symbol string) (dir, name string, ok bool) {
	i := strings.LastIndex(symbol, ".")
	if i < 0 || i == len(symbol)-1 {
		return "", "", false
	}
	return symbol[:i], symbol[i+1:], true
}
