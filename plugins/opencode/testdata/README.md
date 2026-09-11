# cascade-opencode external-contract fixture provenance

Art.2 (12-QUALITY-CONSTITUTION.md): the OpenCode CLI's instruction-file
format is an external contract this plugin does not control. This fixture
is captured FROM the real, installed OpenCode CLI binary, not authored to
a self-invented dialect. It follows the same capture method
internal/context/testdata/goldens/oc/instruction-systempaths.capture.txt
already uses for the sibling generator: OpenCode ships as a single
compiled executable with no readable source tree, so its own embedded
strings are the real counterpart, extracted directly from the binary.

## Capture

- Tool: `opencode` (npm package `opencode-ai`)
- Version: `1.15.5` (`opencode --version`)
- Date: 2026-09-07
- Command: `strings -a <opencode executable>` (the installed
  Node-distributed binary), filtered to lines containing the literal
  `AGENTS.md`.
- No project files, no network calls, no live agent run: this is a static
  read of the binary's own embedded system-prompt text and JSON-schema
  example, so the capture carries no personal or session data to redact.

## Files

- `instructions.capture.txt`: two excerpts extracted verbatim from the
  installed opencode binary: (1) its own system-prompt instruction telling
  the model that files named `AGENTS.md` carry project background and
  conventions and are read from multiple locations in a project, and (2)
  its config-schema example listing `"AGENTS.md"` as an `instructions`
  entry. Together they establish that OpenCode reads a file named exactly
  `AGENTS.md`, the same name internal/context's OCInstructionWriter
  targets (agentsFileName, internal/context/gen_agents.go).

opencode_test.go's TestCascadeOpencodeInstructionFixture asserts the
literal string `AGENTS.md` appears in this capture in the two forms
documented above, in the no-network unit lane (Art.7), proving the golden
this plugin installs targets a filename the real CLI is documented (by its
own shipped text) to read.
