// Package build (this file): the one-audit-log gate. Every production
// audit.Log is built by cmd/cascade/audit_log.go, which runs the secrets
// substitution pass over each appended record and shares one Log per store
// per process. A second constructor call anywhere else would be a Log that
// stores credentials unredacted, or a second Log that forks the chain.
//
// The rule is RED against a seeded fixture and GREEN on the real tree. Test
// files are exempt: a test may build a bare Log over a fixture store.
package build

import (
	"strings"
	"testing"
)

const auditImportPath = cascadeModulePath + "/internal/audit"

// auditCtorViolations returns every shipping file outside internal/audit and
// cmd/cascade/audit_log.go that names audit.New or audit.NewWithRedactor,
// through any import alias or as a function value.
func auditCtorViolations(srcs []archSource) []string {
	var bad []string
	for _, s := range srcs {
		if s.rel == "cmd/cascade/audit_log.go" || underDir(s.rel, "internal/audit") {
			continue
		}
		if selectorRefs(s.file, auditImportPath, "New")+selectorRefs(s.file, auditImportPath, "NewWithRedactor") > 0 {
			bad = append(bad, s.rel)
		}
	}
	return bad
}

func TestArchAuditNewOnlyInHelper(t *testing.T) {
	if bad := auditCtorViolations(shippingSources(t, archModuleRoot(t))); len(bad) > 0 {
		t.Fatalf("shipping files build an audit.Log outside cmd/cascade/audit_log.go; use openAuditLog:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func TestArchAuditNewDetectsViolation(t *testing.T) {
	srcs := seededSources(t, map[string]string{
		"cmd/cascade/hooks.go":          "package main\nimport \"" + auditImportPath + "\"\nvar _ = audit.New(nil, nil, nil)\n",
		"cmd/cascade/alias.go":          "package main\nimport a \"" + auditImportPath + "\"\nvar f = a.NewWithRedactor\n",
		"cmd/cascade/dot.go":            "package main\nimport . \"" + auditImportPath + "\"\n",
		"internal/jobs/own.go":          "package jobs\nimport \"" + auditImportPath + "\"\nvar _ = audit.New(nil, nil, nil)\n",
		"cmd/cascade/audit_log.go":      "package main\nimport \"" + auditImportPath + "\"\nvar _ = audit.NewWithRedactor\n",
		"internal/audit/redact.go":      "package audit\nimport \"" + auditImportPath + "\"\nvar _ = audit.New\n",
		"cmd/cascade/other_package.go":  "package main\nimport \"other/audit\"\nvar _ = audit.New\n",
		"cmd/cascade/uses_only_type.go": "package main\nimport \"" + auditImportPath + "\"\nvar _ audit.Writer\n",
	})
	got := strings.Join(auditCtorViolations(srcs), ",")
	want := "cmd/cascade/alias.go,cmd/cascade/dot.go,cmd/cascade/hooks.go,internal/jobs/own.go"
	if got != want {
		t.Fatalf("audit constructor violations = %s, want %s", got, want)
	}
}
