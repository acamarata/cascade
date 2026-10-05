// Purpose: prove endpoint redaction and cause detachment at backup connect.
// Inputs: the shared URL table and a local invalid-port MinIO failure.
// Outputs: assertions on redacted values, classified Kind and Unwrap chain.
// Constraints: untagged; no network, real custody, or copied form table.
// SPORT: internal.backup.targets.s3.
package targets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBackupS3RedactEndpointCoversURLForms(t *testing.T) {
	checkS3RedactionReviewForms(t)

	c := strings.Join([]string{"s3", "cr3t"}, "")
	raw, err := os.ReadFile("../../../providers/internal/dsnredact/testdata/dsn-forms.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Forms []struct {
			Name, DSN string
			Forbid    []string
		}
	}
	if err := json.Unmarshal(raw, &table); err != nil || len(table.Forms) == 0 {
		t.Fatal("shared form table must decode and contain rows")
	}
	pct := ""
	for i := range len(c) {
		pct += fmt.Sprintf("%%%02X", c[i])
	}
	r := strings.NewReplacer("{c}", c, "{c1}", c[1:], "{pct}", pct,
		"{part}", strings.ReplaceAll(c, "c", "%63"), "{pw1}", c+"1", "{pw2}", c+"2", "{hostPw}", "cvh"+"0st")
	count := 0
	for _, form := range table.Forms {
		if !strings.HasPrefix(form.DSN, "postgres") {
			continue
		}
		count++
		t.Run(form.Name, func(t *testing.T) {
			dsn := r.Replace(form.DSN)
			if strings.ContainsAny(dsn, "{}") {
				t.Fatal("unexpanded URL table token")
			}
			forbidden := []string{c}
			for _, value := range form.Forbid {
				forbidden = append(forbidden, r.Replace(value))
			}
			checkBackupS3Redaction(t, dsn, forbidden)
		})
	}
	if count == 0 {
		t.Fatal("shared form table has no URL forms")
	}
	t.Logf("checked %d shared URL forms", count)
}

// Purpose: cover review's username-token and escaped-password forms; Inputs: test handle; Outputs: fail-closed redaction assertions; Constraints: no credential logs; SPORT: internal.backup.targets.s3.
func checkS3RedactionReviewForms(t *testing.T) {
	t.Helper()
	for _, raw := range []string{"http://s3cr3t@host/s3cr3t", "http://u:s3%20cr3t@host/s3%20cr3t"} {
		if got := redactS3Endpoint(raw); got != "s3://<redacted>" {
			t.Errorf("redactS3Endpoint(%q) = %q, want placeholder", raw, got)
		}
	}
}

// Purpose: reject source-level wrapping or formatting of minio errors; Inputs: both S3 implementations; Outputs: failing file/line; Constraints: untagged AST-only scan; SPORT: internal.backup.targets.s3.
func TestS3MinioErrorsAreNotWrappedOrFormatted(t *testing.T) {
	paths := []string{"s3.go", "../../../providers/s3/s3.go"}
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for _, line := range minioErrorWrapLines(fset, fn) {
				t.Errorf("%s:%d wraps or formats a minio-go error", path, line)
			}
		}
	}
}

// Purpose: find tainted minio errors passed to wrappers; Inputs: one AST function; Outputs: source lines; Constraints: tracks call results and ListObjects.Err; SPORT: internal.backup.targets.s3.
func minioErrorWrapLines(fset *token.FileSet, fn *ast.FuncDecl) []int {
	tainted := map[string]bool{}
	if fn.Name.Name == "wrapConnError" && fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
		for _, name := range fn.Type.Params.List[0].Names {
			tainted[name.Name] = true
		}
	}
	var lines []int
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if isErrorWrapper(n) && hasMinioError(n.Args, tainted) {
				lines = append(lines, fset.Position(n.Pos()).Line)
			}
		case *ast.AssignStmt:
			minioCall := len(n.Rhs) == 1 && isMinioErrorCall(n.Rhs[0])
			for i, lhs := range n.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				tainted[id.Name] = minioCall && i == len(n.Lhs)-1
				if !minioCall && len(n.Rhs) == len(n.Lhs) {
					tainted[id.Name] = hasMinioError([]ast.Expr{n.Rhs[i]}, tainted)
				}
			}
		case *ast.RangeStmt:
			if isMinioMethodCall(n.X, "ListObjects") {
				if id, ok := n.Value.(*ast.Ident); ok {
					tainted[id.Name] = true
				}
			}
		}
		return true
	})
	return lines
}

// Purpose: identify minio-go calls returning errors; Inputs: AST expression; Outputs: whether its call name is guarded; Constraints: method names match this package's API use; SPORT: internal.backup.targets.s3.
func isMinioErrorCall(expr ast.Expr) bool {
	if isMinioMethodCall(expr, "New") {
		call, _ := expr.(*ast.CallExpr)
		selector, _ := call.Fun.(*ast.SelectorExpr)
		pkg, _ := selector.X.(*ast.Ident)
		return pkg != nil && pkg.Name == "minio"
	}
	for _, name := range []string{"BucketExists", "PutObject", "GetObject", "Stat", "RemoveObject"} {
		if isMinioMethodCall(expr, name) {
			return true
		}
	}
	return false
}

// Purpose: match a selected method call; Inputs: AST expression and name; Outputs: call match; Constraints: syntax-only; SPORT: internal.backup.targets.s3.
func isMinioMethodCall(expr ast.Expr, name string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == name
}

// Purpose: detect tainted errors inside expressions; Inputs: expressions and taint set; Outputs: presence flag; Constraints: follows AST descendants; SPORT: internal.backup.targets.s3.
func hasMinioError(exprs []ast.Expr, tainted map[string]bool) bool {
	for _, expr := range exprs {
		found := false
		ast.Inspect(expr, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				found = found || tainted[value.Name]
			case *ast.SelectorExpr:
				if value.Sel.Name == "Err" {
					if id, ok := value.X.(*ast.Ident); ok && tainted[id.Name] {
						found = true
					}
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// Purpose: classify calls that could preserve minio errors; Inputs: AST call; Outputs: whether to inspect its arguments; Constraints: wrappers only; SPORT: internal.backup.targets.s3.
func isErrorWrapper(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	return (pkg.Name == "cascade" && (selector.Sel.Name == "Wrap" || selector.Sel.Name == "Wrapf")) ||
		(pkg.Name == "fmt" && selector.Sel.Name == "Errorf")
}

// checkBackupS3Redaction checks nonempty output without logging secrets.
func checkBackupS3Redaction(t *testing.T, dsn string, forbidden []string) {
	t.Helper()
	got := redactS3Endpoint(dsn)
	if got == "" {
		t.Fatal("redactor returned empty output")
	}
	for _, value := range forbidden {
		if value == "" || strings.Contains(got, value) {
			t.Error("redactor retained a forbidden value or the fixture is empty")
		}
	}
	if _, err := url.Parse(dsn); err != nil && got != "s3://<redacted>" {
		t.Error("unparseable endpoint must return the fixed placeholder")
	}
	if got == "s3://<redacted>" {
		return
	}
	u, err := url.Parse(got)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		t.Error("redacted endpoint must have no userinfo, query, or fragment")
	}
}

func TestBackupS3ConnectErrorDetachesCause(t *testing.T) {
	c := strings.Join([]string{"s3", "cr3t"}, "")
	// Loopback bypasses proxies; MinIO retries each local failure for about eight seconds.
	accessKey, secretKey := c+"-key", c+"-secret"
	cfg := S3Config{Endpoint: "http://127.0.0.1:99999?password=" + c,
		Bucket: c, AccessKeyID: accessKey, SecretAccessKey: secretKey}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := minio.New("127.0.0.1:99999", &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Region: "us-east-1",
	})
	if err != nil {
		t.Fatal("fixture must pass client construction")
	}
	_, cause := client.BucketExists(ctx, cfg.Bucket)
	if cause == nil || !strings.Contains(cause.Error(), c) || classifyS3Err(cause) != cascade.KindUnavailable {
		t.Fatal("fixture must produce a canary-bearing, unavailable connect failure")
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Capability acquisition never reads this fail-closed, unbound vault.
	engine, err := egress.NewEngine(egress.DefaultRegistry(), &secrets.EgressVault{}, detector)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewS3Target(ctx, cfg, engine)
	if target != nil || err == nil || !cascade.HasKind(err, classifyS3Err(cause)) {
		t.Fatal("connect must return its classified Kind and no target")
	}
	if !strings.Contains(err.Error(), "targets: s3: connecting to ") {
		t.Fatal("fixture did not reach the connect-failure wrap")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), c) || strings.Contains(e.Error(), accessKey) || strings.Contains(e.Error(), secretKey) {
			t.Error("connect error chain contains the canary")
		}
	}
	if errors.Unwrap(err) != nil {
		t.Error("connect retained its raw cause")
	}
}
