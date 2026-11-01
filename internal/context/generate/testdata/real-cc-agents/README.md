# Real claude CLI agent and skill frontmatter

`agent-fixture.md` and `skill-fixture.md` are the subagent and skill files
the installed claude CLI CLI writes when it scaffolds a plugin. The
generated agents and skills must carry exactly these frontmatter keys
(`TestFrontmatterMatchesRealCC`): `name`, `description`, `tools` for an agent
and `name`, `description` for a skill.

## Provenance

- Tool: claude CLI CLI (native install)
- Version: 2.1.316
- Binary sha256: 64c327e9 2b2c5e0f 78c0a980 c5a51812 1e1f0d14 0ca771ca 5dd4488d 1a2dcb3f
  (written in eight groups of eight hex digits; remove the spaces to compare)
- Capture date: 2026-11-01
- Method: read from the binary as raw bytes; each file is the return value of a plugin scaffold function (the template literals that begin `---\nname: example` and `---\nname: ${e}`), with the `—` escape decoded to the dash it evaluates to and nothing else changed. `${e}` is the scaffold's skill-name parameter, kept as is.

## Gap

The installed CLI offers no non-interactive, credential-free way to list
the agents and skills a project defines: `claude agents --json` lists
sessions, not agent definitions, and the `/agents` and skill listings are
interactive. So the fixtures are the CLI's own scaffold templates taken from
the binary, not a listing produced by running it. Only the frontmatter keys
are compared; the TODO bodies are the vendor's scaffold text.

## Scrub

Captured under a fresh temp HOME and working directory with no credential
variable. The files hold no user path or email address;
`TestRealCCCapturesScrubbed` fails on a macOS or Linux home-directory
path or an email in any
file here or in the hook capture.
