# Cascade

Local-first AI agent runtime: one CLI and daemon for agents, context, model routing
(Conductor), plugins, and storage that runs the same from a laptop to a server.

**Status: v2 is in early development on a clean sheet.** The complete v1 implementation
(released through v1.16) is archived read-only at
[acamarata/cascade-v1](https://github.com/acamarata/cascade-v1).

MIT licensed.

## Building from source

Cascade builds with the Go toolchain, version 1.26.2 or newer, and has no CGO or
external build dependencies.

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade
go build ./...
```

Supported release platforms:

| OS | Architecture |
|---|---|
| darwin | arm64 |
| darwin | amd64 |
| linux | amd64 |
| linux | arm64 |
| windows | amd64 |

Cross-compile for any of them with `GOOS` and `GOARCH`, for example
`GOOS=linux GOARCH=arm64 go build ./...`.
