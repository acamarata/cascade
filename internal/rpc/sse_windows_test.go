//go:build windows

// Purpose: retired (Windows parity pass 2). ServeHTTP no longer
//   special-cases GOOS: the real refusal lives at the daemon layer
//   (cmd/cascade's platformDaemonStart/Run windows build, proven by
//   internal/daemon/daemon_windows_test.go), which never mounts this
//   handler on Windows at all. The 501-asserting test this file used to
//   carry (TestSSEHandler_Windows_Returns501) tested a behavior sse.go no
//   longer has; kept as an empty, tracked file rather than a git
//   deletion, which this pass has no standing to perform (no git write
//   commands). Whoever next touches this tree with git access should
//   `git rm` it.

package rpc
