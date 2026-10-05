# lane_outcome fixtures

Each file is one HTTP response the lane-outcome tests replay through a fake
Transport. Every file carries its own `provenance` field; the three kinds are
kept apart on purpose. The openai file is a copy of a literal the openai driver
tests already hold; its message is shortened, and the full vendor text is in
`providers/openai/testdata/fuzz/` (`seed_response.json`). Nothing here was
captured by a new provider call:

| File | Kind | Source |
|---|---|---|
| `openai_401_live.json` | byte copy of an in-tree test literal (not a capture made here) | `openaiLive401` at `providers/openai/openai_test.go:60`, a shortened vendor message |
| `gemini_403_billing_disabled_transcribed.json` | transcribed from documentation | `providers/gemini/testdata/error_403_5xx_recorded.json` |
| `gemini_400_api_key_invalid_constructed.json` | constructed | the invalid-key shape in `providers/gemini/testdata/README.md` |

No fixture here carries a vendor reset header or a live 429. The 429 cases
the tests use are built in code and labelled constructed at their use site.
