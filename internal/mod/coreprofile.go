package mod

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// ErrNoCoreProfile indicates the core profile is missing, empty, or unparseable.
var ErrNoCoreProfile = errors.New("core profile missing")

// CoreMembers parses the source bytes of a core_profile.go file and returns
// a map of declared member names. It expects a package-level variable named
// coreProfile initialized with a composite literal of string literals.
func CoreMembers(src []byte) (map[string]bool, error) {
	if len(src) == 0 {
		return nil, ErrNoCoreProfile
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.AllErrors)
	if err != nil {
		return nil, ErrNoCoreProfile
	}

	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}

		for _, spec := range genDecl.Specs {
			valSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for i, name := range valSpec.Names {
				if name.Name == "coreProfile" {
					if i < len(valSpec.Values) {
						compLit, ok := valSpec.Values[i].(*ast.CompositeLit)
						if !ok {
							continue
						}

						members := make(map[string]bool)
						for _, elt := range compLit.Elts {
							lit, ok := elt.(*ast.BasicLit)
							if !ok || lit.Kind != token.STRING {
								continue
							}
							unquoted, err := strconv.Unquote(lit.Value)
							if err == nil {
								members[unquoted] = true
							}
						}

						if len(members) == 0 {
							return nil, ErrNoCoreProfile
						}
						return members, nil
					}
				}
			}
		}
	}

	return nil, ErrNoCoreProfile
}

// IsCore reports whether the given name is present in the provided core members map.
func IsCore(name string, members map[string]bool) bool {
	if members == nil {
		return false
	}
	return members[name]
}
