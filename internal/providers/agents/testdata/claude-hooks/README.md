# claude-hooks provenance

Live captures of the hook payloads the first-party harness client writes on
stdin to a command hook. The relay (`hookrelay.go`) reads only fields that
appear in these files.

| | |
|---|---|
| Tool | the first-party harness client (`claude`) |
| Version | 2.1.288 |
| Captured | 2026-10-14 |
| Ticket | P1-AGT-14 |
| Method | a throwaway git directory with a project-scoped hook for each of `PreToolUse`, `PostToolUse` and `Stop` whose command appended its stdin to a file; the client was run non-interactively (`-p`, `--setting-sources project`, a small model) twice: once to Read a file, once to run a failing shell command and Write a file. The bytes are the files here, reformatted with `json.dumps(indent=2)`. |

Three substitutions only, so no operator path or account appears in a public
repository: the client's config directory became `/home/agent/.config/claude`,
the scratch tree became `/work/tree`, and the encoded project segment of
`transcript_path` (the client's dash-joined form of the scratch tree path)
became `-work-tree`. Session, prompt and tool-use ids are as captured (they
identify a throwaway session).

| File | Event | Tool |
|---|---|---|
| `pretooluse-read.json` | PreToolUse | Read |
| `posttooluse-read.json` | PostToolUse | Read |
| `posttooluse-write.json` | PostToolUse | Write |
| `stop.json` | Stop | none |

## Observed facts the relay relies on

- `PreToolUse` carries `tool_name`, `tool_input`, `tool_use_id`; no `tool_response`, no `duration_ms`.
- `PostToolUse` adds `tool_response` and `duration_ms` (milliseconds).
- `Stop` carries neither `tool_name` nor `tool_use_id`.
- A tool call that fails (the shell command exiting non-zero) fired `PreToolUse` and no `PostToolUse` in the same run, so a `PostToolUse` frame always describes a call that succeeded and the payload has no exit code field.
- File tools put the target in `tool_input.file_path`, as an absolute path.
- Every payload carries `cwd`; the relay does not use it. It reads the project root from
  `CLAUDE_PROJECT_DIR` in the hook process environment (set by the harness), not from the payload,
  so the captures here do not record it.
