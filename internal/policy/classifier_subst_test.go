package policy

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifierSubstInHeredoc checks executable and literal heredoc bodies.
func TestClassifierSubstInHeredoc(t *testing.T) {
	t.Run("expanded", func(t *testing.T) {
		mustClassify(t, "cat <<EOF\n$(rm -rf ~)\nEOF\n", L4)
	})
	t.Run("quoted", func(t *testing.T) {
		mustClassify(t, "cat <<'EOF'\n$(rm -rf ~)\nEOF\n", L0)
	})
}

// TestClassifierSubstInRedirectWord checks substitutions in input filenames.
func TestClassifierSubstInRedirectWord(t *testing.T) {
	t.Run("expanded", func(t *testing.T) { mustClassify(t, "cat < $(rm -rf ~)", L4) })
	t.Run("literal", func(t *testing.T) { mustClassify(t, "cat < notes.txt", L0) })
}

// TestClassifierSubstInHereString checks substitutions in inline input.
func TestClassifierSubstInHereString(t *testing.T) {
	t.Run("expanded", func(t *testing.T) { mustClassify(t, "cat <<< $(rm -rf ~)", L4) })
	t.Run("literal", func(t *testing.T) { mustClassify(t, "cat <<< hello", L0) })
}

// TestClassifierSubstInAssignIndex checks commands executed in array indices.
func TestClassifierSubstInAssignIndex(t *testing.T) {
	t.Run("expanded", func(t *testing.T) { mustClassify(t, "a[$(rm -rf ~)]=1", L4) })
	t.Run("literal", func(t *testing.T) { mustClassify(t, "a[1]=2", L1) })
}

// TestClassifierProcSubstInParamExpRefused checks the parser's literal fallback.
func TestClassifierProcSubstInParamExpRefused(t *testing.T) {
	cmd := "ls ${x:-<(touch /tmp/pwn)}"
	got, err := classify(t, cmd)
	if got != L4 || !errors.Is(err, ErrClassifyUnknown) {
		t.Fatalf("Classify(%q) = %s, %v; want L4 and classify-unknown", cmd, got, err)
	}
	if !strings.Contains(err.Error(), "ProcSubst") {
		t.Fatalf("refusal %q must name ProcSubst", err)
	}
}

// TestClassifierArgSubstUnchanged preserves argument and parameter substitutions.
func TestClassifierArgSubstUnchanged(t *testing.T) {
	t.Run("argument", func(t *testing.T) { mustClassify(t, "ls $(rm -rf ~)", L4) })
	t.Run("parameter", func(t *testing.T) { mustClassify(t, "ls ${x:-$(touch /tmp/pwn)}", L2) })
}
