package build

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

type rpcDocLocation struct{ page, anchor string }

type rpcSource struct {
	rel  string
	file *ast.File
	fset *token.FileSet
}

func rpcSourceFiles(root string) ([]string, error) {
	files, err := gitLsFilesFn(root)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "rpcindex: listing tracked files")
	}
	var out []string
	for _, file := range sortedCopy(files) {
		file = filepath.ToSlash(file)
		if strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") && !strings.Contains("/"+file+"/", "/testdata/") {
			out = append(out, file)
		}
	}
	return out, nil
}

func rpcParseSources(root string) ([]rpcSource, map[string]constLiteralMap, error) {
	files, err := rpcSourceFiles(root)
	if err != nil {
		return nil, nil, err
	}
	var out []rpcSource
	cache := map[string]constLiteralMap{}
	for _, rel := range files {
		path := filepath.Join(root, rel)
		b, err := readFileFn(path)
		if err != nil {
			return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "rpcindex: reading %s", rel)
		}
		fs := token.NewFileSet()
		f, err := parseGoFileFn(fs, path, b)
		if err != nil {
			return nil, nil, cascade.Wrapf(cascade.KindInvalidInput, err, "rpcindex: parsing %s", rel)
		}
		if f == nil {
			return nil, nil, cascade.Newf(cascade.KindInvalidInput, "rpcindex: parsing %s returned no syntax tree", rel)
		}
		dir := filepath.Dir(rel)
		if cache[dir] == nil {
			cache[dir] = constLiteralMap{}
		}
		collectFileConsts(f, cache[dir])
		out = append(out, rpcSource{rel, f, fs})
	}
	return out, cache, nil
}

func rpcDocPriority(path string) int {
	if path == "docs/rpc-protocol.md" {
		return 0
	}
	if strings.HasPrefix(path, "docs/reference/rpc/") {
		return 1
	}
	return 2
}

func rpcDocumentedMethods(root string, methods []string) (map[string]rpcDocLocation, error) {
	files, err := DocTruthScope(root)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		a, b := rpcDocPriority(files[i]), rpcDocPriority(files[j])
		if a != b {
			return a < b
		}
		return files[i] < files[j]
	})
	out := map[string]rpcDocLocation{}
	for _, rel := range files {
		if rel == "docs/reference/rpc-methods.md" || rel == "docs/reference/config-keys.md" || strings.HasPrefix(rel, "docs/cli/") {
			continue
		}
		content, err := readFileFn(filepath.Join(root, rel))
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "rpcindex: reading %s", rel)
		}
		for method, anchor := range rpcMethodsInDoc(string(content), methods) {
			if _, exists := out[method]; !exists {
				out[method] = rpcDocLocation{rel, anchor}
			}
		}
	}
	return out, nil
}

func rpcMethodsInDoc(content string, methods []string) map[string]string {
	out := map[string]string{}
	headings := parseHeadings(content)
	byLine := map[int]heading{}
	for _, h := range headings {
		byLine[h.line] = h
	}
	anchor := ""
	for i, line := range parseDocLines(content) {
		if line.fenced {
			continue
		}
		h, isHeading := byLine[i+1]
		if isHeading {
			anchor = h.slug
		}
		for _, method := range methods {
			if _, exists := out[method]; exists {
				continue
			}
			if (isHeading && stripBackticks(h.text) == method) || strings.HasPrefix(line.text, "| `"+method+"` |") {
				out[method] = anchor
			}
		}
	}
	return out
}
