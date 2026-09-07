# providers/embeddings fixture provenance (Art.2.2)

## What this ticket's fixtures are, and why they are not a wire-shape capture

ProviderEmbedder (`apibacked.go`) is an adapter, not a driver: it never
decodes bytes off the wire itself. It calls `provider.ModelProvider.Embed`
and receives an already-decoded `[][]float32` - the JSON decode for every
real vendor happens inside the J/S-19.T2-T5 driver packages
(`providers/anthropic`, `providers/openai`, `providers/gemini`,
`providers/ollama`), each of which carries its own provenance-stamped
fixtures under its own `testdata/`. This ticket adds no new parser or
decoder (06-FORGE-SPEC.md §5 rule 7), so Art.2.4's "self-dialect-only"
warning - which exists to catch a test that invents a plausible-looking
wire shape instead of using a real one - does not apply at this layer: the
values `apibacked_test.go` exercises are post-decode `[]float32` vectors,
not a JSON body standing in for one.

`recordedEmbedFixture()` in `apibacked_test.go` is three pairwise-distinct,
hand-authored 3-wide vectors (`{0.11,0.22,0.33}`, `{0.44,0.55,0.66}`,
`{0.77,0.88,0.99}`). Their only required property is pairwise distinctness,
which is what makes the positional-correspondence assertion in
`TestProviderEmbedderRecordedFixture` meaningful (a reordered, deduplicated,
or padded batch would fail it; identical vectors would not). They are not a
claim about any vendor's real vector values.

## The real external contract this ticket rides on

The external contract Art.2 exists to protect - "does a real provider
answer an embed call the way this code assumes" - is already covered, for
the wire layer, by the landed drivers' own provenance:

- `providers/openai/testdata/README.md` - openai-compat `/embeddings`
  response shape, captured/documented per-vendor (OpenAI, Moonshot/Kimi,
  zai, DeepSeek).
- `providers/gemini/testdata/README.md` - `batchEmbedContents` response
  shape, transcribed from Google's published API reference (this build
  environment has no outbound network access, recorded there as an honest
  gap against a live capture).
- `providers/ollama/testdata/README.md` and `providers/anthropic/
  testdata/README.md` - the `/api/embed` response shape and the real,
  landed `KindUnsupported` refusal (`providers/anthropic/anthropic.go`'s
  `Embed`) this ticket's `TestProviderEmbedderCapabilityGate` reproduces
  verbatim as its want-error fixture.

This same build environment - no outbound network access, per the sibling
drivers' own README files above - means this ticket cannot itself perform
a fresh live capture at either layer. `TestProviderEmbedderCapabilityGate`
uses anthropic's real, already-landed refusal message and Kind rather than
inventing one, which is the closest a decode-free adapter can come to an
Art.2 real counterpart at the unit-test layer.

## The tagged live lane

`integration_test.go` (`//go:build integration`,
`TestProviderEmbedderLiveAPI`) is the genuine end-to-end real-counterpart
proof for this type: with `PROVIDER_EMBED_TEST=1` and a real
`CASCADE_OPENAI_TEST_KEY`, it builds a real `providers/openai` driver and
drives `ProviderEmbedder.Embed` against it over the live network. Without
either, it reports an explicit skip reason and never silently passes as a
real-counterpart proof (Art.2). This ticket's own CI/local verification
runs did not set `PROVIDER_EMBED_TEST=1`, so this lane has been exercised
only via its skip path in that verification, consistent with every sibling
J/S-19 driver's own integration lane in this repository.
