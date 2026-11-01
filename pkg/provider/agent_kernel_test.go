package provider

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestPolicyKernelFileConst pins the kernel constants' shapes: a clean,
// relative slash path under .cascade, a 64-hex lowercase digest and the
// 4 KiB ceiling. The digest's value is pinned against the rendered kernel
// in internal/context/policy (TestPolicyKernelHashConst).
func TestPolicyKernelFileConst(t *testing.T) {
	if PolicyKernelFile != ".cascade/policy-kernel.md" {
		t.Fatalf("PolicyKernelFile = %q", PolicyKernelFile)
	}
	if path.Clean(PolicyKernelFile) != PolicyKernelFile || path.IsAbs(PolicyKernelFile) ||
		strings.Contains(PolicyKernelFile, `\`) {
		t.Fatalf("PolicyKernelFile %q is not a clean relative slash path", PolicyKernelFile)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(PolicyKernelSHA256) {
		t.Fatalf("PolicyKernelSHA256 %q is not 64 lowercase hex", PolicyKernelSHA256)
	}
	if PolicyKernelMaxBytes != 4096 {
		t.Fatalf("PolicyKernelMaxBytes = %d, want 4096", PolicyKernelMaxBytes)
	}
}
