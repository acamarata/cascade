package provider

// Purpose: the policy-kernel constants an agent driver needs to place and
//   verify .cascade/policy-kernel.md without importing the generator
//   (contract:policy-kernel).
// Inputs: none.
// Outputs: the kernel file's repository-relative path, its size ceiling and
//   the sha256 of its canonical bytes.
// Constraints: imports nothing from internal/ (Art.10.2), so this package
//   cannot render the kernel itself. internal/context/policy's
//   TestPolicyKernelHashConst keeps PolicyKernelSHA256 equal to the hash of
//   policy.RenderKernelFile(); a kernel wording change fails that test until
//   this constant moves with it.
// SPORT: pkg.provider.PolicyKernel/ADD (P1-GEN-06).

// PolicyKernelFile is where the canonical kernel file lives, relative to a
// repository root, in slash form.
const PolicyKernelFile = ".cascade/policy-kernel.md"

// PolicyKernelSHA256 is the lowercase hex sha256 of the canonical rendered
// kernel file.
const PolicyKernelSHA256 = "9a67a9f8fa4158fda65747f98f64b7d212c7f101ab10c97df517516e88f2d854"

// PolicyKernelMaxBytes is the largest kernel file a driver accepts.
const PolicyKernelMaxBytes = 4096
