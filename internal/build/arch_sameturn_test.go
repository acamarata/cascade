// Package build (this file): the same-turn ledger's single-writer gate. A
// same-turn authorization overrides a deny-list entry, so the only shipping
// code that may record one is the attested policy.authorize handler in
// cmd/cascade/daemon_unix_policy_authorize.go. A second writer (a chat
// ingress that parsed AUTHORIZE text, a tool that authorized on a request
// field) would open the window without an attestation.
//
// The check is syntactic, so it is deliberately wide and ignores imports:
// every file in package main shares package scope, so a file that never
// imports internal/policy can still reach the ledger through a field such as
// w.Deps.Policy.SameTurn. In every shipping file other than the writer and
// the ledger's own two files, named exactly, AuthorizeWithExpiry is named
// nowhere, and no Authorize is called with the ledger's three arguments,
// called with one argument (a multi-value call can spread into three), or
// taken as a method value or expression; nor is the ledger's entry type
// named, its entries map assigned into, or either method named in a string
// (reflection by name). A syntactic gate cannot be complete: an alias of the
// map or reflection by method index is left to review. The elevation gates'
// Authorize(ctx, verb) shapes are left alone. The rule is RED against seeded
// fixtures and GREEN on the real tree. Test files are exempt.
package build

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// sameTurnWriterFile is the one shipping file allowed to record a same-turn
// authorization.
const sameTurnWriterFile = "cmd/cascade/daemon_unix_policy_authorize.go"

// sameTurnLedgerFiles are the ledger's own files, where Authorize delegates
// to AuthorizeWithExpiry. They are named exactly: a prefix would exempt any
// later internal/policy/sameturn_*.go file.
var sameTurnLedgerFiles = map[string]bool{
	"internal/policy/sameturn.go":        true,
	"internal/policy/sameturn_expiry.go": true,
}

// otherAuthorizeValues are method values of a different Authorize, named by
// file and exact expression, that the syntactic rule cannot tell from the
// ledger's. Each entry allows exactly one occurrence in one file: the
// conductor passes its own door check (*Executor).Authorize to FanOut as an
// AuthorizeFn.
var otherAuthorizeValues = map[string]string{
	"internal/conductor/execute.go": "e.Authorize",
}

// selectorNamed counts the selector expressions in f whose selected name is
// name, whatever the receiver.
func selectorNamed(f *ast.File, name string) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			n++
		}
		return true
	})
	return n
}

// isMethodExpr reports whether sel is a method expression on a pointer type,
// (*T).Authorize, which takes the receiver as its first argument.
func isMethodExpr(sel *ast.SelectorExpr) bool {
	p, ok := sel.X.(*ast.ParenExpr)
	if !ok {
		return false
	}
	_, star := p.X.(*ast.StarExpr)
	return star
}

// ledgerAuthorizeRefs counts the Authorize selectors in f that can reach the
// ledger's: a call with the ledger's three arguments, with one argument (a
// three-result call spread into it), with four (a method expression taking
// the receiver first), or through a (*T).Authorize method expression at all.
// The elevation gates' Authorize(ctx, verb) and the backup authorizer's
// five-argument call are other shapes. So does a method value never called
// directly, parenthesised or not, beyond the one occurrence
// otherAuthorizeValues allows for rel.
func ledgerAuthorizeRefs(rel string, f *ast.File) int {
	called := map[*ast.SelectorExpr]bool{}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun := call.Fun
		for {
			p, isParen := fun.(*ast.ParenExpr)
			if !isParen {
				break
			}
			fun = p.X
		}
		if sel, ok := fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Authorize" {
			called[sel] = true
			if k := len(call.Args); k == 1 || k == 3 || k == 4 || isMethodExpr(sel) {
				n++
			}
		}
		return true
	})
	exempt := 1
	ast.Inspect(f, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Authorize" || called[sel] {
			return true
		}
		if x, isIdent := sel.X.(*ast.Ident); isIdent && otherAuthorizeValues[rel] == x.Name+".Authorize" && exempt > 0 {
			exempt--
			return true
		}
		n++
		return true
	})
	return n
}

// otherEntriesWrites are assignments into another internal/policy type's own
// entries map, named by file and exact receiver: the approval queue's.
var otherEntriesWrites = map[string]string{
	"internal/policy/approval_queue_batch.go": "q",
}

// ledgerBackdoors counts the ways f could record an authorization without
// an Authorize selector: the ledger's entry type named outside its files, a
// string naming either method (which reflection's MethodByName would need)
// and, inside internal/policy (the only package that can reach the
// unexported map), an assignment into a field named entries other than the
// one exempted for rel in otherEntriesWrites.
func ledgerBackdoors(rel string, f *ast.File) int {
	inPolicy := underDir(rel, "internal/policy")
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.Ident:
			if x.Name == "sameTurnEntry" {
				n++
			}
		case *ast.BasicLit:
			if v, err := strconv.Unquote(x.Value); x.Kind == token.STRING && err == nil &&
				(v == "Authorize" || v == "AuthorizeWithExpiry") {
				n++
			}
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				ix, ok := lhs.(*ast.IndexExpr)
				if !ok || !inPolicy {
					continue
				}
				if sel, ok := ix.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "entries" {
					if id, isIdent := sel.X.(*ast.Ident); !isIdent || otherEntriesWrites[rel] != id.Name {
						n++
					}
				}
			}
		}
		return true
	})
	return n
}

// sameTurnWriterViolations returns every shipping file other than the writer
// and the ledger's own files that can record a same-turn authorization, and
// whether the writer file records one at all.
func sameTurnWriterViolations(srcs []archSource) (bad []string, writerFound bool) {
	for _, s := range srcs {
		switch {
		case s.rel == sameTurnWriterFile:
			// The writer itself records through exactly one AuthorizeWithExpiry
			// call and nothing else, so a second helper in it is caught too.
			n := selectorNamed(s.file, "AuthorizeWithExpiry")
			writerFound = n > 0
			if n > 1 || ledgerAuthorizeRefs(s.rel, s.file) > 0 || ledgerBackdoors(s.rel, s.file) > 0 {
				bad = append(bad, s.rel)
			}
			continue
		case sameTurnLedgerFiles[s.rel]:
			continue
		}
		if selectorNamed(s.file, "AuthorizeWithExpiry") > 0 || ledgerAuthorizeRefs(s.rel, s.file) > 0 ||
			ledgerBackdoors(s.rel, s.file) > 0 {
			bad = append(bad, s.rel)
		}
	}
	return bad, writerFound
}

func TestArchSameTurnLedgerSingleWriter(t *testing.T) {
	bad, found := sameTurnWriterViolations(shippingSources(t, archModuleRoot(t)))
	if len(bad) > 0 {
		t.Fatalf("shipping files record a same-turn authorization outside %s:\n  %s",
			sameTurnWriterFile, strings.Join(bad, "\n  "))
	}
	if !found {
		t.Fatalf("%s no longer records a same-turn authorization; the gate would pass vacuously", sameTurnWriterFile)
	}

	srcs := seededSources(t, sameTurnSeeds())
	bad, found = sameTurnWriterViolations(srcs)
	want := "cmd/cascade/chat_authorize.go," + sameTurnWriterFile + ",cmd/cascade/tool_value.go," +
		"cmd/cascade/wire_expr.go,cmd/cascade/wire_expr_call.go,cmd/cascade/wire_expr_spread.go,cmd/cascade/wire_noimport.go,cmd/cascade/wire_paren.go," +
		"cmd/cascade/wire_reflect.go,cmd/cascade/wire_spread.go," +
		"internal/conductor/execute.go,internal/conductor/fanout_ledger.go,internal/conversation/ingress.go," +
		"internal/policy/approval_queue_batch.go,internal/policy/chat_copy.go,internal/policy/chat_override.go," +
		"internal/policy/ingress_authorize.go,internal/policy/sameturn_ingress.go"
	if got := strings.Join(bad, ","); got != want || !found {
		t.Fatalf("seeded violations = %s (writer found %t), want %s (writer found)", got, found, want)
	}
}

// sameTurnSeeds is every seeded writer shape the gate must catch, plus the
// legitimate shapes (the ledger's own files, a gate's two-argument
// Authorize, the conductor's one method value) it must leave alone.
func sameTurnSeeds() map[string]string {
	seeds := sameTurnSeedsMain()
	for rel, src := range sameTurnSeedsInternal() {
		seeds[rel] = src
	}
	return seeds
}

// sameTurnSeedsMain is the seeded call and value shapes.
func sameTurnSeedsMain() map[string]string {
	imp := "import \"" + policyImportPath + "\"\n"
	return map[string]string{
		"cmd/cascade/chat_authorize.go": "package main\n" + imp +
			"func f(l *policy.SameTurnLedger) { _, _ = l.Authorize(nil, policy.Subject{}, \"x\") }\n",
		"cmd/cascade/tool_value.go": "package main\n" + imp +
			"func f(l *policy.SameTurnLedger) { _ = l.Authorize }\n",
		"internal/conversation/ingress.go": "package conversation\n" +
			"func f(l interface{ AuthorizeWithExpiry() }) { l.AuthorizeWithExpiry() }\n",
		"internal/policy/ingress_authorize.go": "package policy\n" +
			"func f(l *SameTurnLedger) { _, _ = l.Authorize(nil, Subject{}, \"x\") }\n",
		"cmd/cascade/node_upgrade.go": "package main\n" + imp +
			"func f(g interface{ Authorize(a, b any) error }) { _ = g.Authorize(nil, \"node.upgrade\") }\n",
		"internal/policy/sameturn.go": "package policy\n" +
			"func (l *SameTurnLedger) Authorize() { l.AuthorizeWithExpiry() }\n",
		"internal/policy/sameturn_expiry.go": "package policy\n" +
			"func (l *SameTurnLedger) Authorize() { l.AuthorizeWithExpiry() }\n",
		// No policy import: package main reaches the ledger through a field.
		"cmd/cascade/wire_noimport.go": "package main\n" +
			"func f(w *daemonWiring) { _, _ = w.Deps.Policy.SameTurn.Authorize(nil, mcpToolSubject, \"git push\") }\n",
		"cmd/cascade/wire_spread.go": "package main\n" +
			"func f(w *daemonWiring) { _, _ = w.Deps.Policy.SameTurn.Authorize(args()) }\n",
		"cmd/cascade/wire_expr.go": "package main\n" + imp +
			"var authorize = (*policy.SameTurnLedger).Authorize\n",
		"cmd/cascade/wire_expr_call.go": "package main\n" + imp +
			"func f(l *policy.SameTurnLedger) { _, _ = (*policy.SameTurnLedger).Authorize(l, nil, mcpToolSubject, \"x\") }\n",
		"cmd/cascade/wire_expr_spread.go": "package main\n" + imp +
			"func f(l *policy.SameTurnLedger) { _, _ = (*policy.SameTurnLedger).Authorize(l, args()) }\n",
		"cmd/cascade/wire_paren.go": "package main\n" +
			"func f(w *daemonWiring) { _, _ = (w.Deps.Policy.SameTurn.Authorize)(nil, mcpToolSubject, \"x\") }\n",
	}
}

// sameTurnSeedsInternal is the seeded exemption, reflection, map-write and
// writer-file shapes.
func sameTurnSeedsInternal() map[string]string {
	imp := "import \"" + policyImportPath + "\"\n"
	return map[string]string{
		// The conductor's exemption is one expression in one file.
		"internal/conductor/fanout_ledger.go": "package conductor\n" +
			"func f(e *Executor) { _ = e.Authorize }\n",
		"internal/conductor/execute.go": "package conductor\n" +
			"func f(e *Executor) { _ = e.Authorize }\nfunc g(e *SameTurnLedger) { fn := e.Authorize; _ = fn }\n",
		// Reflection, and a direct write into the ledger's map.
		"cmd/cascade/wire_reflect.go": "package main\nimport \"reflect\"\n" +
			"func f(w *daemonWiring) { reflect.ValueOf(w.Deps.Policy.SameTurn).MethodByName(\"Authorize\") }\n",
		"internal/policy/chat_override.go": "package policy\n" +
			"func f(l *SameTurnLedger, k string) { l.entries[k] = sameTurnEntry{} }\n",
		"internal/policy/approval_queue_batch.go": "package policy\n" +
			"func f(q *approvalBatch, l *SameTurnLedger) { q.entries[\"a\"] = nil; l.entries[\"k\"] = l.entries[\"o\"] }\n",
		"internal/policy/chat_copy.go": "package policy\n" +
			"func f(l *SameTurnLedger, k, o string) { l.entries[k] = l.entries[o] }\n",
		// A ledger-named file the exact list does not cover.
		"internal/policy/sameturn_ingress.go": "package policy\n" +
			"func f(l *SameTurnLedger) { _, _ = l.Authorize(nil, Subject{}, \"x\") }\n",
		sameTurnWriterFile: "package main\n" + imp +
			"func f(l *policy.SameTurnLedger) { l.AuthorizeWithExpiry() }\n" +
			"func g(l *policy.SameTurnLedger) { l.AuthorizeWithExpiry() }\n",
	}
}
