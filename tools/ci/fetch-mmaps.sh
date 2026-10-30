#!/usr/bin/env bash
# fetch-mmaps downloads a pjerra/tb-server-data release of server navmesh files, verifies its SHA256SUMS and unpacks
# the tar.zst archives into one flat folder (TURTLE_AGENT_MMAPS).
#
# The release may split an archive into parts: <name>.tar.zst.part-aa, .part-ab, ... are joined in name order into
# <name>.tar.zst before verification. SHA256SUMS may list the joined archive or the parts, or both; every checksum
# whose file exists is checked, and at least one must verify.
#
# Usage: GH_TOKEN=... tools/ci/fetch-mmaps.sh TAG REPO WORKDIR DEST
#   TAG      release tag, e.g. data-2026-10-29
#   REPO     owner/name of the private data repository
#   WORKDIR  scratch folder for the download (created)
#   DEST     the folder to unpack into (created; files already there are kept)
set -euo pipefail

if (($# != 4)); then
	echo "usage: GH_TOKEN=... $0 TAG REPO WORKDIR DEST" >&2
	exit 2
fi
tag=$1
repo=$2
work=$3
dest=$4
[[ -n ${GH_TOKEN:-} ]] || {
	echo "fetch-mmaps: GH_TOKEN is not set" >&2
	exit 2
}
mkdir -p "$work" "$dest"

echo "fetch-mmaps: downloading $repo release $tag"
gh release download "$tag" --repo "$repo" --dir "$work" --clobber
sums=$work/SHA256SUMS
[[ -f $sums ]] || {
	echo "fetch-mmaps: the release has no SHA256SUMS asset" >&2
	exit 1
}

# Join split parts (only when the unsplit archive is absent).
shopt -s nullglob
for first in "$work"/*.tar.zst.part-*; do
	base=${first%.part-*}
	[[ -f $base ]] && continue
	parts=("$base".part-*)
	cat "${parts[@]}" >"$base"
	echo "fetch-mmaps: joined ${#parts[@]} part(s) into $(basename "$base")"
done

# Verify what is here. --ignore-missing lets SHA256SUMS name an archive that is split (its parts are checked) or a
# joined archive (its parts are checked too); a mismatch still fails.
verified=$(cd "$work" && sha256sum -c --ignore-missing SHA256SUMS)
printf '%s\n' "$verified"
if ! grep -q ': OK$' <<<"$verified"; then
	echo "fetch-mmaps: no checksum in SHA256SUMS matched a downloaded file" >&2
	exit 1
fi

# Unpack every archive into a staging folder, then flatten it into DEST: the release's per-folder tars may or may not
# carry a top-level folder, and the navmesh files' names (001.mmap, 0013542.mmtile, ...) are unique.
stage=$(mktemp -d "$work/unpack.XXXXXX")
trap 'rm -rf "$stage"' EXIT
for archive in "$work"/*.tar.zst; do
	[[ -f $archive ]] || continue
	tar --zstd -xf "$archive" -C "$stage"
done
count=0
while IFS= read -r -d '' f; do
	cp -n "$f" "$dest/"
	count=$((count + 1))
done < <(find "$stage" -type f -print0)
if ((count == 0)); then
	echo "fetch-mmaps: no file unpacked" >&2
	exit 1
fi
echo "fetch-mmaps: $count file(s) in $dest"
