# cascade-codex external-contract fixture provenance

Art.2 (12-QUALITY-CONSTITUTION.md): the Codex CLI's AGENTS.md format is an
external contract this plugin does not control. This fixture is captured
FROM a real Codex CLI parse pass, not authored to a self-invented dialect.

## Capture

- Tool: `codex-cli`
- Version: `0.144.2` (`codex --version`)
- Date: 2026-09-07
- Command: `CODEX_HOME=<isolated empty dir> codex debug prompt-input`, run
  from a scratch working directory containing only the `agents-md-fixture/
  input.AGENTS.md` file below (copied to `AGENTS.md` at that directory's
  root).
- `CODEX_HOME` was pointed at an empty, freshly created directory (rather
  than the operator's real `~/.codex`) so the capture contains no personal
  configuration or global instructions — only Codex's own built-in system
  prompt scaffolding plus the fixture's own project-level AGENTS.md
  content.

## Files

- `input.AGENTS.md`: the instruction golden this plugin's Generate seam
  would install. Matches the shape internal/context's CXInstructionWriter
  renders (a `cascade:generate-instructions` managed block).
- `prompt-input.capture.txt`: `codex debug prompt-input`'s JSON output,
  reduced to the one prompt-input message whose text embeds
  `input.AGENTS.md`'s content verbatim under the
  `# AGENTS.md instructions for <path>` / `<INSTRUCTIONS>...</INSTRUCTIONS>`
  wrapper Codex's real CLI produces. The absolute scratch path is replaced
  with the `<WORKSPACE>` placeholder, matching the sanitization convention
  internal/context/testdata/goldens/cx/prompt-input.capture.txt already
  uses for the same capture family.

codex_test.go's TestCascadeCodexAGENTSMdFixture replays this pair: it
asserts input.AGENTS.md's exact bytes appear, verbatim and unmodified,
inside the `<INSTRUCTIONS>...</INSTRUCTIONS>` wrapper of
prompt-input.capture.txt, in the no-network unit lane (Art.7).
