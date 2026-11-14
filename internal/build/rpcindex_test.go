package build

import (
	"strings"
	"testing"
)

// TestRPCMethodReference_EveryMethodDocumented builds the method rows over the
// real tree and requires zero findings, a page and anchor for every row, and
// the delegation and hook methods among them.
func TestRPCMethodReference_EveryMethodDocumented(t *testing.T) {
	root := rpcMethodGateModuleRoot(t)
	rows, findings, err := BuildRPCMethodRows(root)
	if err != nil {
		t.Fatalf("BuildRPCMethodRows: %v", err)
	}
	for _, f := range findings {
		t.Errorf("finding: %s", f.Detail)
	}
	if len(rows) < 40 {
		t.Fatalf("rows = %d, want at least 40", len(rows))
	}
	byMethod := map[string]RPCMethodRow{}
	for _, row := range rows {
		byMethod[row.Method] = row
		if row.DocPage == "" || row.DocAnchor == "" {
			t.Errorf("%s: page %q anchor %q", row.Method, row.DocPage, row.DocAnchor)
		}
		if strings.HasSuffix(row.DocPage, "rpc-methods.md") || strings.HasPrefix(row.DocPage, "docs/cli/") {
			t.Errorf("%s documented in %s", row.Method, row.DocPage)
		}
	}
	for _, m := range []string{"conductor.delegate", "conductor.delegate_wait", "hooks.list", "hooks.fires"} {
		if _, ok := byMethod[m]; !ok {
			t.Errorf("missing row %s", m)
		}
	}
}
