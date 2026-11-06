// Purpose: pin DelegationNeed and ClassifyDelegation to contract:delegate-rpc:
//   the exact declared shape, the defaults, the mutation-needs-a-job rule and
//   the refusals outside the closed sets.
// Inputs: none.
// Outputs: none.
// Constraints: ErrInvalidParam is asserted by identity and by message, never
//   by errors.Is alone.
// SPORT: conductor.delegate/ADD (P1-AGT-08a).

package conductor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// delegateFieldSig renders one field's type from the parsed source.
func delegateFieldSig(t *testing.T, expr ast.Expr) string {
	t.Helper()
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return delegateFieldSig(t, e.X) + "." + e.Sel.Name
	}
	t.Fatalf("unexpected type expression %T", expr)
	return ""
}

// delegateListSigs renders a parameter or field list, one entry per name.
func delegateListSigs(t *testing.T, list *ast.FieldList) []string {
	t.Helper()
	var out []string
	for _, f := range list.List {
		sig := delegateFieldSig(t, f.Type)
		if len(f.Names) == 0 {
			out = append(out, sig)
		}
		for _, name := range f.Names {
			out = append(out, name.Name+" "+sig)
		}
	}
	return out
}

// delegateSpecNames lists the identifiers one general declaration introduces
// and, for a struct type, records its fields under structs.
func delegateSpecNames(t *testing.T, d *ast.GenDecl, structs map[string][]string) []string {
	t.Helper()
	var names []string
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			names = append(names, s.Name.Name)
			if st, ok := s.Type.(*ast.StructType); ok {
				structs[s.Name.Name] = delegateListSigs(t, st.Fields)
			}
		case *ast.ValueSpec:
			for _, name := range s.Names {
				names = append(names, name.Name)
				if d.Tok == token.VAR && name.Name != "ErrInvalidParam" {
					t.Errorf("package-level var %s: only ErrInvalidParam may exist", name.Name)
				}
			}
		}
	}
	return names
}

func TestDelegationNeedContractShape(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "delegate_classify.go", nil, 0)
	if err != nil {
		t.Fatalf("parse delegate_classify.go: %v", err)
	}
	structs := map[string][]string{}
	funcs := map[string]*ast.FuncDecl{}
	var names []string
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			names = append(names, delegateSpecNames(t, d, structs)...)
		case *ast.FuncDecl:
			names = append(names, d.Name.Name)
			if d.Recv == nil {
				funcs[d.Name.Name] = d
			}
		}
	}

	wantFields := []string{
		"TaskClass string",
		"MinQuality string",
		"Deadline string",
		"Mutation bool",
		"JobID string",
		"Sensitivity provider.SensitivityTier",
	}
	if got := structs["DelegationNeed"]; !reflect.DeepEqual(got, wantFields) {
		t.Errorf("DelegationNeed fields = %v, want %v", got, wantFields)
	}

	fn := funcs["ClassifyDelegation"]
	if fn == nil {
		t.Fatal("ClassifyDelegation is not declared")
	}
	params := delegateListSigs(t, fn.Type.Params)
	if len(params) != 1 || !strings.HasSuffix(params[0], "DelegationNeed") {
		t.Errorf("ClassifyDelegation params = %v, want one DelegationNeed", params)
	}
	wantResults := []string{"DelegationNeed", "string", "error"}
	if got := delegateListSigs(t, fn.Type.Results); !reflect.DeepEqual(got, wantResults) {
		t.Errorf("ClassifyDelegation results = %v, want %v", got, wantResults)
	}

	allowed := map[string]bool{"DelegationNeed": true, "ErrInvalidParam": true, "ClassifyDelegation": true}
	for _, name := range names {
		if ast.IsExported(name) && !allowed[name] {
			t.Errorf("exported identifier %s is outside the contract", name)
		}
	}
}

type classifyDefaultsCase struct {
	name       string
	in         DelegationNeed
	want       DelegationNeed
	wantPolicy string
}

// classifyDefaultsCases is the table for TestClassifyDelegationDefaults.
func classifyDefaultsCases() []classifyDefaultsCase {
	return []classifyDefaultsCase{
		{
			name: "reason only",
			in:   DelegationNeed{Mutation: false},
			want: DelegationNeed{
				TaskClass:   "reason",
				MinQuality:  "standard",
				Deadline:    "interactive",
				Sensitivity: provider.SensitivityRestricted,
			},
			wantPolicy: provider.ToolPolicyReadonly,
		},
		{
			name: "mutation",
			in:   DelegationNeed{Mutation: true, JobID: "job-1"},
			want: DelegationNeed{
				TaskClass:  "code",
				MinQuality: "standard",
				Deadline:   "interactive",
				Mutation:   true,
				JobID:      "job-1",
			},
			wantPolicy: provider.ToolPolicyAll,
		},
		{
			name: "explicit values kept",
			in: DelegationNeed{
				TaskClass:   "review",
				MinQuality:  "high",
				Deadline:    "batch",
				Sensitivity: provider.SensitivityInternal,
			},
			want: DelegationNeed{
				TaskClass:   "review",
				MinQuality:  "high",
				Deadline:    "batch",
				Sensitivity: provider.SensitivityInternal,
			},
			wantPolicy: provider.ToolPolicyReadonly,
		},
	}
}

func TestClassifyDelegationDefaults(t *testing.T) {
	tests := classifyDefaultsCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, policy, err := ClassifyDelegation(tt.in)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("need = %+v, want %+v", got, tt.want)
			}
			if policy != tt.wantPolicy {
				t.Errorf("policy = %q, want %q", policy, tt.wantPolicy)
			}
		})
	}
}

// assertDelegationRefused pins the single refusal shape: the zero need, an
// empty policy and the ErrInvalidParam sentinel itself.
func assertDelegationRefused(t *testing.T, in DelegationNeed) {
	t.Helper()
	got, policy, err := ClassifyDelegation(in)
	if err != ErrInvalidParam { //nolint:errorlint // identity is the contract: the sentinel is never wrapped.
		t.Fatalf("ClassifyDelegation(%+v) err = %v, want ErrInvalidParam itself", in, err)
	}
	if ErrInvalidParam.Kind != cascade.KindInvalidInput {
		t.Errorf("err kind = %v, want KindInvalidInput", ErrInvalidParam.Kind)
	}
	if ErrInvalidParam.Msg != "conductor: invalid delegation parameter" {
		t.Errorf("err message = %q", ErrInvalidParam.Msg)
	}
	if got := err.Error(); got != "invalid-input: conductor: invalid delegation parameter" {
		t.Errorf("err text = %q", got)
	}
	if got != (DelegationNeed{}) {
		t.Errorf("need = %+v, want the zero DelegationNeed", got)
	}
	if policy != "" {
		t.Errorf("policy = %q, want empty", policy)
	}
}

func TestClassifyDelegationMutationNeedsJob(t *testing.T) {
	assertDelegationRefused(t, DelegationNeed{Mutation: true})
	assertDelegationRefused(t, DelegationNeed{Mutation: true, JobID: " "})

	got, policy, err := ClassifyDelegation(DelegationNeed{Mutation: false, JobID: "job-1"})
	if err != nil {
		t.Fatalf("reason-only need carrying a job: err = %v", err)
	}
	if got.JobID != "job-1" {
		t.Errorf("JobID = %q, want job-1 unchanged", got.JobID)
	}
	if policy != provider.ToolPolicyReadonly {
		t.Errorf("policy = %q, want readonly", policy)
	}
}

func TestClassifyDelegationRefusesOutsideClosedSets(t *testing.T) {
	assertDelegationRefused(t, DelegationNeed{TaskClass: "cook"})
	assertDelegationRefused(t, DelegationNeed{MinQuality: "low"})
	assertDelegationRefused(t, DelegationNeed{Deadline: "now"})
	assertDelegationRefused(t, DelegationNeed{Sensitivity: provider.SensitivityTier(9)})

	// The boundary of every closed set, with and without a mutation: each
	// member is kept unchanged, and only Mutation true ever yields policy all.
	var classes []string
	for class := range taskClasses {
		classes = append(classes, class)
	}
	if len(classes) != 9 {
		t.Fatalf("taskClasses has %d members, want the closed nine", len(classes))
	}
	tiers := []provider.SensitivityTier{
		provider.SensitivityRestricted,
		provider.SensitivityLocalOnly,
		provider.SensitivityInternal,
		provider.SensitivityPublic,
	}
	for _, class := range classes {
		for _, quality := range []string{"standard", "high", "max"} {
			for _, deadline := range []string{"interactive", "batch"} {
				for _, tier := range tiers {
					base := DelegationNeed{
						TaskClass:   class,
						MinQuality:  quality,
						Deadline:    deadline,
						JobID:       "job-1",
						Sensitivity: tier,
					}
					assertDelegationPolicy(t, base, provider.ToolPolicyReadonly)
					base.Mutation = true
					assertDelegationPolicy(t, base, provider.ToolPolicyAll)
				}
			}
		}
	}
}

// assertDelegationPolicy requires in to pass through unchanged with policy.
func assertDelegationPolicy(t *testing.T, in DelegationNeed, want string) {
	t.Helper()
	got, policy, err := ClassifyDelegation(in)
	if err != nil {
		t.Fatalf("ClassifyDelegation(%+v) err = %v", in, err)
	}
	if got != in {
		t.Errorf("need = %+v, want %+v unchanged", got, in)
	}
	if policy != want {
		t.Errorf("ClassifyDelegation(%+v) policy = %q, want %q", in, policy, want)
	}
}
