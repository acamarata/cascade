package nodes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type fakeControllerBindingBackend struct {
	binding ControllerBinding
	found   bool
	err     error
}

func (b fakeControllerBindingBackend) Load() (ControllerBinding, bool, error) {
	return b.binding, b.found, b.err
}

func TestResolveRoleMatrix(t *testing.T) {
	designationTestHome(t)
	for _, tc := range []struct {
		name, raw, conflict string
		designated          bool
		role                Role
	}{
		{"designation only", "", "", true, RoleController},
		{"complete binding only", `{"endpoint":"https://controller.invalid","controller_pubkey_b64":"pinned"}`, "", false, RoleNode},
		{"both", `{"endpoint":"https://controller.invalid","controller_pubkey_b64":"pinned"}`, "both", true, ""},
		{"neither", "", "none", false, ""},
		{"binding without endpoint", `{"controller_pubkey_b64":"pinned"}`, "binding-incomplete", false, ""},
		{"binding without key", `{"endpoint":"https://controller.invalid"}`, "binding-incomplete", false, ""},
		{"empty binding", `{}`, "binding-incomplete", false, ""},
		{"designated with incomplete binding", `{}`, "binding-incomplete", true, ""},
		{"whitespace endpoint", `{"endpoint":" ","controller_pubkey_b64":"pinned"}`, "binding-incomplete", false, ""},
		{"whitespace key", `{"endpoint":"https://controller.invalid","controller_pubkey_b64":" "}`, "binding-incomplete", false, ""},
		{"load error", `{`, "load-error", false, ""},
		{"designation load error", "", "load-error", false, ""},
		{"nil backend", "", "no-source", false, ""},
		{"typed nil backend", "", "no-source", false, ""},
		{"non-source fake", "", "no-source", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := roleMatrixBackend(t, tc.name, tc.raw, tc.designated)
			role, err := ResolveRole(b)
			assertRoleStatus(t, b, role, tc.conflict)
			if tc.conflict == "" {
				if err != nil || role != tc.role {
					t.Fatalf("got %q, %v; want %q", role, err, tc.role)
				}
				return
			}
			wantKind(t, err, cascade.KindIntegrity)
			if role == RoleController || !strings.Contains(err.Error(), tc.conflict) {
				t.Fatalf("got %q, %v; want conflict %s", role, err, tc.conflict)
			}
		})
	}
}

func assertRoleStatus(t *testing.T, b ControllerBindingBackend, role Role, kind string) {
	t.Helper()
	got, conflict := RoleStatus(b)
	if got != role || (conflict == nil) != (kind == "") {
		t.Fatalf("RoleStatus = %q, %+v; want %q, %s", got, conflict, role, kind)
	}
	if conflict != nil && conflict.Kind != kind {
		t.Fatalf("conflict = %+v; want %s", conflict, kind)
	}
}

type roleSourceProbe struct {
	fakeControllerBindingBackend
	source DesignationBackend
}

func (b roleSourceProbe) DesignationBackend() DesignationBackend { return b.source }

func TestResolveRoleWrapsLoadErrors(t *testing.T) {
	want := errors.New("load failed")
	for _, b := range []ControllerBindingBackend{
		roleSourceProbe{source: &designationProbe{loadErr: want}},
		roleSourceProbe{fakeControllerBindingBackend: fakeControllerBindingBackend{err: want}, source: &designationProbe{}},
	} {
		role, err := ResolveRole(b)
		wantKind(t, err, cascade.KindIntegrity)
		if role == RoleController || !errors.Is(err, want) || !strings.Contains(err.Error(), "load-error") {
			t.Fatalf("got %q, %v; want wrapped load-error", role, err)
		}
	}
}

func TestRoleStatusMissingDesignationSource(t *testing.T) {
	var typedNil *designationProbe
	for _, d := range []DesignationBackend{nil, typedNil} {
		assertRoleStatus(t, roleSourceProbe{source: d}, "", "no-source")
	}
}

type panickingRoleBackend struct{ roleSourceProbe }

func (panickingRoleBackend) Load() (ControllerBinding, bool, error) { panic("unreadable binding") }

func TestRoleStatusBackendPanic(t *testing.T) {
	b := panickingRoleBackend{roleSourceProbe{source: &designationProbe{}}}
	assertRoleStatus(t, b, "", "load-error")
	role, err := ResolveRole(b)
	wantKind(t, err, cascade.KindIntegrity)
	if role == RoleController || !strings.Contains(err.Error(), "load-error") {
		t.Fatalf("got %q, %v; want load-error", role, err)
	}
}

func roleMatrixBackend(t *testing.T, name, raw string, designated bool) ControllerBindingBackend {
	t.Helper()
	if name == "nil backend" {
		return nil
	}
	if name == "typed nil backend" {
		return (*fakeControllerBindingBackend)(nil)
	}
	if name == "non-source fake" {
		return fakeControllerBindingBackend{}
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		if err := os.WriteFile(filepath.Join(dir, "nodes", "controller_binding.json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if designated {
		if err := NewFileDesignationBackend(dir).Write(testDesignation()); err != nil {
			t.Fatal(err)
		}
	}
	if name == "designation load error" {
		if err := os.WriteFile(filepath.Join(dir, "nodes", "controller_designation.json"), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return NewFileControllerBindingBackend(dir)
}

func TestNodeRoleRefusesSchedulerRPCs(t *testing.T) {
	for _, method := range GuardedMethods {
		err := RequireController(RoleNode, method)
		if err == nil {
			t.Fatalf("RequireController(node, %q) = nil, want ErrNotController", method)
		}
		var kerr *cascade.Error
		if !errors.As(err, &kerr) || kerr.Kind != cascade.KindPermissionDenied {
			t.Fatalf("RequireController(node, %q) err = %v, want KindPermissionDenied", method, err)
		}
	}
}

func TestControllerRoleAllowsSchedulerRPCs(t *testing.T) {
	for _, method := range GuardedMethods {
		if err := RequireController(RoleController, method); err != nil {
			t.Fatalf("RequireController(controller, %q) = %v, want nil", method, err)
		}
	}
}

func TestRoleFromContextDefaultsToNode(t *testing.T) {
	if got := RoleFromContext(context.Background()); got != RoleNode {
		t.Fatalf("RoleFromContext(bare ctx) = %q, want node (fail-closed)", got)
	}
}

func TestRoleFromContextRoundTrip(t *testing.T) {
	ctx := WithRole(context.Background(), RoleController)
	if got := RoleFromContext(ctx); got != RoleController {
		t.Fatalf("RoleFromContext(WithRole(controller)) = %q, want controller", got)
	}
}
