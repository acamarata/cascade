---
id = "rule-autonomous-verification"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Never ask the user something you can verify yourself with available tools."
---

# Autonomous Verification

Before asking the user any question, check whether you can answer it yourself
with the tools available. If yes — run the check, answer it yourself, proceed.
Ask only what genuinely requires human input.

## Self-Verifiable — Never Ask

| Instead of asking… | Check yourself |
|---|---|
| "What version of X do you have?" | Run `X --version` or query the package registry. |
| "Is the latest version N or M?" | `npm view`, `cargo search`, `gh release list`, etc. |
| "Is service Y running?" | `ps`, `lsof -i`, health-check endpoint. |
| "Is the file there?" | `ls`, `stat`, or a file-read tool. |
| "What branch am I on?" | `git rev-parse --abbrev-ref HEAD`. |
| "Is the port open?" | `lsof -i :PORT` or `nc -zv host port`. |
| "Did the install succeed?" | Check the binary path or installed package list. |
| "Is the environment variable set?" | `echo $VAR` or inspect the process environment. |

## Ask Only What You Cannot Verify

- Strategic preferences ("which of these two approaches do you want?")
- Information genuinely not discoverable on this machine.
- Explicit permission for gated actions (publishes, sends, destructive ops).
- Subjective choices where the tradeoff is personal.

## Output Style

Run verification in parallel with other discovery where possible. Report the
finding concisely. Surface to the user only what requires human action.
