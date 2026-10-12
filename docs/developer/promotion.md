# Release promotion

Promotion turns a verified release candidate (rc) into the final public release. The code lives in `internal/release/promotion` (library) and `internal/tools/promotion` (tool). Run the tool with `go run ./internal/tools/promotion <verb>`.

Exit codes for every verb: 0 ok, 1 refused (the message names the file or the check), 2 bad input. A refusal writes nothing.

## Trust root

The minisign signature on `cascade_checksums.txt` is the trust root. The promotion record is an audit artifact only. The verifier never trusts a digest that a record or a dispatch input claims. It hashes the files it read and compares those hashes.

## Publish the signing key to Pages

The `pages-keys` workflow runs on `workflow_dispatch` only. Its permissions are `contents: read`, `pages: write` and `id-token: write`.

1. Dispatch it on the commit that holds the key.
2. The first step runs `check-key`. It refuses unless `docs/keys/minisign.pub` parses as a minisign public key, is byte-identical to `internal/minisign/release.pub`, and equals the key compiled into the binary.
3. Only then does it upload `docs/keys/` and deploy to Pages.

Run the same check by hand:

    go run ./internal/tools/promotion check-key --pub docs/keys/minisign.pub --embedded internal/minisign/release.pub

## Build the record

    go run ./internal/tools/promotion build --rc-tag v2.Y.Z-rc.N --commit <40 hex> \
      --dir <rc dist> --pubkey docs/keys/minisign.pub --gate-report <path> --out record.json

`build` verifies the signature first. It then checks the trusted comment, the tag, the commit and the checksum lines. It needs all five platform archives. The record lists the five archives, every other checksummed file, the image digests, the digest of the signed release-gate report and the time. `--out` is written only after every check passes.

Fixture and schema test: `internal/build/testdata/promotion-record.json` and `TestPromotionRecordSchema`. Set `CASCADE_PROMOTION_RECORD` to check a real record. A set but missing path fails the test.

## Verify

    go run ./internal/tools/promotion verify --record record.json --staging <dir> --public <dir>

Every file the record covers must be in both directories with the recorded digest. One changed byte, one missing file or one extra file fails and names the file. Staging may also hold `Casks/cascade.rb`.

## Verify the signed set

    go run ./internal/tools/promotion verify-signed --dir <dist-id dir> --pubkey docs/keys/minisign.pub --final-tag v2.Y.Z

This checks, in order:

1. The minisign signature of `cascade_checksums.txt` verifies with the given key.
2. The trusted comment is exactly `cascade <version> checksums`.
3. Every file equals its line in the signed checksums. None is missing and none is extra.
4. `cascade_images.txt` equals its signed line.

A set whose files were re-hashed and whose checksums file was rewritten without a valid signature is refused. So is a set signed by another key, or signed for another version.

`Casks/cascade.rb` sits in `dist-<id>`. It is unsigned and is never published. `verify-signed` allows it and ignores it.

## Images

    go run ./internal/tools/promotion images --dir <dir> --pubkey docs/keys/minisign.pub --final-tag v2.Y.Z --require sha256:<hex>

Image digests come only from the signed `cascade_images.txt`. `--require` refuses a digest that file does not list. `--name` prints the digest for an image name.

## What P1-EXIT-12 runs

After the final release is published, P1-EXIT-12 downloads the public assets and runs `promotion verify --staging <dist-id dir> --public <download dir> --record record.json`. It proves the public bytes equal the staging bytes.

## Promote job

The `promote` job lives in `.github/workflows/release.yml`. It runs on `workflow_dispatch` only, when `record_json` is set. `rc-publish` runs only when `record_json` is empty. Inputs: `final_tag`, `commit_sha`, `record_json` and `notes`.

Job permissions are exactly `contents: write`, `packages: write`, `id-token: write` and `attestations: write`. No other job sets job-level permissions except `rc-publish` (`contents: read`, `packages: write`). A release-config test holds this allowlist, the dispatch inputs and the places the staging secret may appear (step env of those two jobs only).

Steps, in order:

1. Refuse a missing staging remote, a malformed tag or commit, a `commit_sha` that is not the dispatched commit, or a `record_json` over 64 KiB. This runs before checkout.
2. Check out `commit_sha`, so the key at `docs/keys/minisign.pub` is the one committed there.
3. Refuse when `final_tag` already exists on the remote at another commit (`gh` ignores `--target` for an existing tag). Then pull `dist-<commit_sha[:12]>` from the private staging package with oras. The output stays in the runner.
4. `verify-signed` is the first gate. Nothing is attested, released, copied or signed before it passes.
5. Check `record_json` against the verified set. It must name the same `final_tag` and `commit_sha`, its images must equal the signed `cascade_images.txt` lines, and `verify --record` must pass. The record is audit only and never supplies a digest.
6. Attest build provenance over the archives and SBOMs.
7. Create the release with `gh release create`, not as a draft, targeting `commit_sha`, with exactly the verified files. This creates no tag other than the release tag and pushes no branch.
8. Copy the index digest named in the signed `cascade_images.txt` with `oras cp --recursive`, by digest and never by tag, to `ghcr.io/acamarata/cascade:<version>`.
9. Sign every digest in the signed `cascade_images.txt` on the public copy with cosign keyless signing. The step counts the signatures. A digest that file does not list is never copied or signed.

A golden file, `internal/build/testdata/promote-job.golden.yaml`, pins the whole job: every step, key, environment entry and script. Any edit to the job turns `TestPromoteJobMatchesGolden` red until the golden is regenerated on purpose and the change is reviewed.

The job never rebuilds. It uses no `vars.` reference and no `::add-mask::`. The staging remote comes only from the repository secret.
