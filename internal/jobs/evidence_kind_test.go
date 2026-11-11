package jobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestEvidenceKindSetIsBasePlusQA(t *testing.T) {
	kinds := []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview,
		EvidenceAdversarial, EvidenceCIAttestation, EvidenceHumanApproval, EvidenceQA}
	words := []string{"build", "lint", "tests", "review", "adversarial", "ci_attestation", "human_approval", "qa"}
	for i, kind := range kinds {
		if string(kind) != words[i] || !kind.Valid() {
			t.Errorf("kind = %q, Valid = %v; want %q, true", kind, kind.Valid(), words[i])
		}
	}
	for _, kind := range []EvidenceKind{"", "QA", "Qa", "unknown", "bogus"} {
		if kind.Valid() {
			t.Errorf("unexpected valid kind %q", kind)
		}
	}
}

func TestEvidenceKindMovedOutOfEvidenceGo(t *testing.T) {
	for _, tc := range []struct {
		file string
		want [3]int
	}{
		{"evidence.go", [3]int{0, 0, 0}},
		{"evidence_kind.go", [3]int{1, 8, 1}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), tc.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := evidenceKindDeclarations(file); got != tc.want {
				t.Errorf("EvidenceKind [types constants Valid methods] = %v, want %v", got, tc.want)
			}
		})
	}
}

func evidenceKindDeclarations(file *ast.File) (counts [3]int) {
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Name.Name == "EvidenceKind" {
						counts[0]++
					}
				case *ast.ValueSpec:
					kind, ok := spec.Type.(*ast.Ident)
					if decl.Tok == token.CONST && ok && kind.Name == "EvidenceKind" {
						counts[1] += len(spec.Names)
					}
				}
			}
		case *ast.FuncDecl:
			if decl.Name.Name != "Valid" || decl.Recv == nil {
				continue
			}
			receiver := decl.Recv.List[0].Type
			if pointer, ok := receiver.(*ast.StarExpr); ok {
				receiver = pointer.X
			}
			if kind, ok := receiver.(*ast.Ident); ok && kind.Name == "EvidenceKind" {
				counts[2]++
			}
		}
	}
	return counts
}
