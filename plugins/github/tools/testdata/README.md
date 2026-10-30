# Pull-request diff fixtures

`prs-diff/` is a real capture from api.github.com, taken 2026-10-29 for
P1-PLG-05 with an unauthenticated request (no token, so nothing is redacted):

    GET https://api.github.com/repos/octocat/Hello-World/pulls/1
    Accept: application/vnd.github.diff
    X-GitHub-Api-Version: 2022-11-28

- `prs-diff/capture.txt`: the request line, status, content-type, date and
  `x-github-request-id` (CD7C:107624:3A85499:BFF8D40:6AE3D0FF) as GitHub
  returned them.
- `prs-diff/response.diff`: the response body, byte for byte (520 bytes,
  sha256 807e98f3f92408debcb5027b3b51ed45ba084b39855fec2f507e0e60ba171815).

GitHub served status 200 with `application/vnd.github.diff; charset=utf-8`,
the media type `prs.diff` asks for. TestPRDiffRealTranscript decodes this
capture and fails if the status, media type or digest drift. A 406 was not
captured (no public PR reliably triggers it); the 406 mapping is tested
against a synthetic status. internal/plugins/testdata/replayplugin serves the
same body to the wiring tests.
