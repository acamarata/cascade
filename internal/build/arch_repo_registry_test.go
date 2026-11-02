package build

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

type registryAudit struct {
	violations                 []string
	register, signer, elevated int
}

func registryPackages(t *testing.T, pattern string) []*packages.Package {
	t.Helper()
	// Load for one fixed GOOS: the register and signer call sites live in !windows
	// files, so a host-OS load on windows would see no positive controls.
	pkgs, err := packages.Load(&packages.Config{Dir: archModuleRoot(t), Env: append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0"), Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo}, pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("registry audit loaded no packages")
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			t.Fatalf("registry audit type checking: %s", p.Errors[0])
		}
	}
	return pkgs
}

func auditRegistry(pkgs []*packages.Package) registryAudit {
	a := registryAudit{}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			file := filepath.ToSlash(p.Fset.Position(f.Pos()).Filename)
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			auditRegistryFile(p, f, file, &a)
		}
	}
	return a
}

func auditRegistryFile(p *packages.Package, f *ast.File, file string, a *registryAudit) {
	allowed := strings.HasSuffix(file, "/cmd/cascade/wire_ci_repo.go")
	cli := filepath.Base(file) == "ci_repo.go"
	for _, im := range f.Imports {
		if cli && (strings.Contains(im.Path.Value, "/pkg/envelope") || strings.Contains(im.Path.Value, "/internal/ci\"")) {
			a.violations = append(a.violations, "CLI import: "+file)
		}
		if filepath.Base(file) == "wire_ci_repo.go" && strings.Contains(im.Path.Value, "spf13/cobra") {
			a.violations = append(a.violations, "daemon cobra: "+file)
		}
	}
	auditRegistryReferences(p, f, file, allowed, cli, a)
	auditRegistryElevation(f, cli, a)
}

func auditRegistryReferences(p *packages.Package, f *ast.File, file string, allowed, cli bool, a *registryAudit) {
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := p.TypesInfo.Uses[id]
		if obj == nil {
			return true
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return true
		}
		sig, _ := fn.Type().(*types.Signature)
		if sig != nil && sig.Recv() != nil && strings.HasSuffix(types.TypeString(sig.Recv().Type(), nil), "jobs.RepoRegistry") {
			switch fn.Name() {
			case "Register":
				a.register++
				if !allowed {
					a.violations = append(a.violations, "Register: "+file)
				}
			case "RecordTrustFetch":
				a.violations = append(a.violations, "RecordTrustFetch: "+file)
			case "WithTrustRecordSigner":
				a.signer++
				if !allowed {
					a.violations = append(a.violations, "WithTrustRecordSigner: "+file)
				}
			}
		}
		if cli && registryCLIForbidden(fn.Name()) {
			a.violations = append(a.violations, "CLI reference: "+fn.Name())
		}
		if filepath.Base(p.Fset.Position(fn.Pos()).Filename) == "wire_ci_repo.go" && !allowed {
			a.violations = append(a.violations, "daemon reference: "+fn.Name())
		}
		return true
	})
}

func auditRegistryElevation(f *ast.File, cli bool, a *registryAudit) {
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := c.Fun.(*ast.Ident)
		if cli && ok && id.Name == "callElevated" {
			for _, arg := range c.Args {
				if s, ok := arg.(*ast.BasicLit); ok && s.Kind == token.STRING && s.Value == `"ci.repo_register"` {
					a.elevated++
				}
			}
		}
		return true
	})
}

func registryCLIForbidden(name string) bool {
	switch name {
	case "Register", "Registration", "OperatorRegistered", "TrustFetches", "WithTrustRecordSigner", "NewRepoRegistry", "ApplyRepoRegistrySchema", "productionEnvelopeSigner", "RegistrationBaseline":
		return true
	}
	return false
}

// TestArchRepoRegistryRegisterCallers binds trust writers to daemon composition.
func TestArchRepoRegistryRegisterCallers(t *testing.T) {
	a := auditRegistry(registryPackages(t, "./..."))
	if len(a.violations) > 0 {
		t.Fatalf("registry boundary violations: %v", a.violations)
	}
	if a.register == 0 || a.signer == 0 || a.elevated == 0 {
		t.Fatalf("registry boundary missing positive controls: %+v", a)
	}
}

// TestArchRepoRegistryRegisterCallersDetectsViolation exercises every boundary leg.
func TestArchRepoRegistryRegisterCallersDetectsViolation(t *testing.T) {
	a := auditRegistry(registryPackages(t, "./internal/build/testdata/arch-repo-registry"))
	for _, want := range []string{"Register:", "RecordTrustFetch:", "WithTrustRecordSigner:", "CLI import:", "CLI reference:", "daemon reference: wireCiRepoRPC", "daemon reference: fixtureHelper"} {
		if !strings.Contains(strings.Join(a.violations, "\n"), want) {
			t.Errorf("did not detect %s: %v", want, a.violations)
		}
	}
}
