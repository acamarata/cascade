package nodes

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

// Purpose (this file): the node-originated dispatch verbs are declared
//   AccessNode where they are defined, and the interim owner-only table
//   that used to declare them is gone.

// interimNodeDispatchTable is the deleted owner-only declaration file,
// relative to this package's directory.
var interimNodeDispatchTable = filepath.Join("..", "rpc", "access_table_node_dispatch.go")

// requireNodeAccess reports, by name, a verb that is not declared AccessNode.
func requireNodeAccess(method string) error {
	a, ok := rpc.AccessOf(method)
	if !ok {
		return errors.New(method + ": no access declaration")
	}
	if a != rpc.AccessNode {
		return errors.New(method + ": declared with another access class, want AccessNode")
	}
	return nil
}

// TestNodeDispatchInterimTableDeleted pins the move: the interim table no
// longer exists and claim/report/journal are AccessNode, so the owner
// registry refuses them by class wherever they are mounted.
func TestNodeDispatchInterimTableDeleted(t *testing.T) {
	if _, err := os.Stat(interimNodeDispatchTable); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("os.Stat(%s) = %v, want fs.ErrNotExist: the interim table must be deleted",
			interimNodeDispatchTable, err)
	}
	for _, method := range []string{DispatchClaimMethod, DispatchReportMethod, DispatchJournalMethod} {
		if err := requireNodeAccess(method); err != nil {
			t.Errorf("%v", err)
		}
	}
	// The check itself must fail by name for an undeclared verb and for a
	// verb declared with another class, or the loop above proves nothing.
	for _, method := range []string{"node.dispatch.never-declared", DispatchMethod} {
		err := requireNodeAccess(method)
		if err == nil {
			t.Errorf("requireNodeAccess(%s) = nil, want a refusal", method)
			continue
		}
		if !strings.HasPrefix(err.Error(), method+": ") {
			t.Errorf("requireNodeAccess(%s) = %q, want it to name the verb", method, err)
		}
	}
}
