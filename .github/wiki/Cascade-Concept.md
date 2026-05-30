# The Cascade Concept

AI coding tools are only as useful as the context they have. Without good context, they produce generic code that ignores your conventions, suggests patterns you have already decided against, and asks the same clarifying questions session after session.

The usual workaround is to paste instructions at the start of every conversation, or to maintain per-tool configuration files by hand. Both approaches break down quickly. Paste-at-start context gets stale. Per-tool files diverge. If you use three different tools across five projects, you end up with fifteen configuration files that all say slightly different things.

Cascade solves this with a different approach: one set of source files, automatically resolved for any context, derived into whatever format each tool expects.

## The core idea

Instructions have natural scopes. Some rules apply everywhere: how you handle errors, what your preferred code style is, which patterns you avoid on principle. Others apply only to a specific class of projects: your Next.js projects all follow the App Router conventions. Others apply to one repo: this service uses a specific database schema that nothing else touches.

Cascade models this as a hierarchy of instruction files. Each level of the hierarchy adds specificity. Lower levels can add to or override higher levels, but they never accidentally affect sibling scopes.

The hierarchy has six tiers:

| Tier | Scope | Example file location |
|---|---|---|
| GCI | Your entire machine | `~/.cascade/CASCADE.md` |
| PCI | A portfolio or account | `~/projects/.cascade/CASCADE.md` |
| APC | A project group | `~/projects/my-product/.cascade/CASCADE.md` |
| PPC | A single product | `~/projects/my-product/api/.cascade/CASCADE.md` |
| PRC | A single repo | `~/projects/my-product/api/CASCADE.md` |
| PAC | An app within a repo | `~/projects/my-product/api/web/CASCADE.md` |

When you open a project in an AI tool, Cascade walks up from your working directory, finds all the relevant tiers, merges them according to the conflict resolution rules, and writes the result to the file the tool expects.

## What problems this solves

**Context drift.** Without Cascade, you edit `CLAUDE.md` for one project, forget to update the equivalent file for another, and the two diverge. With Cascade, you edit the source once at the right tier, and all projects that inherit from that tier stay current.

**Tool proliferation.** Each AI tool has its own config file format: `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, `.aider.md`, and so on. Writing and maintaining separate files for each tool is tedious and error-prone. Cascade treats these as derived outputs, not source of truth. You write once; it writes many.

**Inconsistent behavior.** When different tools see different instructions, they behave differently on the same project. Cascade makes the context that every tool sees deterministic and consistent.

**Discoverability.** With dozens of instruction files spread across a large project, it is hard to know which rules are active for any given context. `cascade search` lets you query your instructions the same way a tool does, so you can see what context is actually being applied.

## What Cascade does not do

Cascade manages your instruction files. It does not execute code, run builds, or interact with AI providers. It is not an AI agent or a workflow automation tool. It is infrastructure for your instructions, not a replacement for the tools you already use.

The AI tool you choose, how you configure its model, and how you interact with it are all outside Cascade's scope. Cascade just makes sure that tool always has good context to work with.

## Design goals

- **Local first.** Your instructions are Markdown files on your disk. No account, no cloud sync, no vendor dependency.
- **Vendor neutral.** Cascade supports any tool that reads a config file. Adding support for a new tool is a matter of teaching Cascade what file to write and where.
- **Transparent.** You can read every file Cascade writes. There is no hidden state.
- **Composable.** The tier hierarchy composes naturally. Adding a new project-level tier does not require touching any other tier.
