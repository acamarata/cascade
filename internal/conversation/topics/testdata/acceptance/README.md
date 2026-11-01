# Acceptance fixtures — topic auto-filing

Two fixture files live here (P1-CPA-12). Both are synthetic. Neither carries
owner or personal content.

| File | Source | What it proves | Pinned by |
|---|---|---|---|
| `four-topic-conversation.jsonl` | `syntheticCorpus()` in `../../segmenter_harness_test.go` (P1-E21-W5-S45-T2), exported one `json.Marshal(CorpusRecord)` per line | The auto-filing mechanism over the closed `{alpha, beta, gamma, delta}` vocabulary, as a file a live run can read | `TestFourTopicFixtureMatchesSyntheticCorpus` (byte for byte) |
| `four-topic-live.jsonl` | Hand-authored, 2026-11-01. Line 1 is a `provenance` object; every later line is one user turn `{role, text, topic}` | Natural-language turns over four topics (gardening, baking, astronomy, cycling) for a real classifier lane | `TestFourTopicLiveFixtureHasProvenance`; replayed by `TestChatThreadsLiveFourFixtureThreads` (`cmd/cascade`, `//go:build live`) and `TestRecallResumeTopicResolutionWithProductionEmbedder` (`internal/retrieval`, `//go:build integration && live`) |

## Provenance

- Tool: `json.Marshal` for the exported corpus; a text editor for the live file.
- Date: 2026-11-01.
- Recording tests: named in the table above.
- Sanitization: none needed; no real conversation was used.

## The real owner corpus

The real S-45.T1 owner corpus still belongs in `../corpus/`.
`acceptanceRealOwnerCorpus` (in `acceptance_autofile_test.go`) skips with a
named citation while that directory is empty. Neither file here substitutes
for it.

## Regenerating

Change `syntheticCorpus()` and this file together. The export test fails when
either side moves alone.
