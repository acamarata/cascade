#!/usr/bin/env bash
# mkbuild.sh DIR VERSION: write an obviously fake build-<id> payload for
# helper_release_test.go: darwin and linux tar.gz archives holding the five
# expected members, a windows "zip" (opaque fake bytes: it is only passed
# through), one fake SBOM per archive, install.sh, cascade_checksums.txt,
# cascade_images.txt, metadata.json (the commit the fake go shim reports) and
# Casks/cascade.rb with the darwin sha256 fields. Archive entry mtimes
# (2001-02-03) differ from the commit time the shim reports (2002-03-04).
set -eu
umask 022
D="$1" V="$2"
mkdir -p "$D/Casks" "$D/.src"
printf 'fake license\n' >"$D/.src/LICENSE"
printf 'fake readme\n' >"$D/.src/README.md"
printf 'fake third-party licenses\n' >"$D/.src/THIRD_PARTY_LICENSES"
for p in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
	printf 'fake cascade %s\n' "$p" >"$D/.src/cascade"
	printf 'fake cascade-github %s\n' "$p" >"$D/.src/cascade-github"
	chmod 0755 "$D/.src/cascade" "$D/.src/cascade-github"
	touch -d 2001-02-03T04:05:06Z "$D/.src/"*
	(cd "$D/.src" && COPYFILE_DISABLE=1 tar -czf "../cascade_${V}_$p.tar.gz" LICENSE README.md THIRD_PARTY_LICENSES cascade cascade-github)
done
printf 'fake windows zip\n' >"$D/cascade_${V}_windows_amd64.zip"
rm -rf "$D/.src"
for a in "$D"/cascade_*; do printf '{"fake":"sbom of %s"}\n' "$(basename "$a")" >"$a.sbom.json"; done
printf '#!/bin/sh\necho fake installer\n' >"$D/install.sh"
(cd "$D" && shasum -a 256 cascade_"$V"_* install.sh >cascade_checksums.txt)
printf 'sha256:%064d  cascade\n' 7 >"$D/cascade_images.txt"
printf '{"project_name":"cascade","version":"%s","commit":"%s"}\n' "$V" 1111111111111111111111111111111111111111 >"$D/metadata.json"
sum() { awk -v n="cascade_${V}_darwin_$1.tar.gz" '$2 == n {print $1}' "$D/cascade_checksums.txt"; }
cat >"$D/Casks/cascade.rb" <<CASK
cask "cascade" do
  version "$V"

  on_macos do
    on_arm do
      sha256 "$(sum arm64)"
    end
    on_intel do
      sha256 "$(sum amd64)"
    end
  end

  binary "cascade"
  binary "cascade-github"
end
CASK
