# gha_reader fixtures

Captured from the real GitHub REST API on 2026-10-30T00:59:10Z, unauthenticated,
with `curl -sS -H "Accept: application/vnd.github+json" https://api.github.com/<path>`.
Bodies are stored verbatim.

| file | path | status |
| --- | --- | --- |
| repo_public.json | /repos/octocat/Hello-World | 200 (private false, visibility public) |
| commit_200.json | /repos/octocat/Hello-World/commits/7fd1a60b01f91b314f59955a4e4d4e80d8edf11d | 200 |
| commit_missing.json | /repos/octocat/Hello-World/commits/0000000000000000000000000000000000000001 | 422 |
| commit_repo_404.json | /repos/octocat/no-such-repo-ci03-zz/commits/7fd1a60b01f91b314f59955a4e4d4e80d8edf11d | 404 |

Used by internal/ci/gha_reader_test.go (P1-CI-03).
