#!/usr/bin/env bash
# Prints the build matrix for one runtime's images (runtime-images.yml), as
# {"include": [{version, base, missing, bases}, ...]}.
#
#   scripts/runtime-image-matrix.sh <versions.json> <image> <all|changed> <base ref>...
#
# A base ref names an upstream image a version's Dockerfile builds FROM, with {base} for the
# version's "base" tag (php:{base}-fpm). Each one is resolved to its current digest: bases lists
# them as ref@digest, the build pins exactly those images, and the image is labelled with them
# (envoryx.base.digests). "changed" leaves out the versions whose published image was built from
# the same digests less than six days ago, so a daily run rebuilds a version the day upstream
# ships a fix and otherwise once a week (Debian packages, extensions and tools change too).
set -euo pipefail

versions=$1 image=$2 mode=$3
shift 3

# The digest of an image index or manifest, or nothing when the registry doesn't answer
# three times.
inspect_digest() {
	local try d
	for try in 1 2 3; do
		d=$(docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' 2>/dev/null | jq -r '.digest // empty' || true)
		if [ -n "$d" ]; then
			echo "$d"
			return
		fi
		[ "$try" = 3 ] || sleep 5
	done
}

# The digest of an upstream ref, once per ref. Docker Hub throttles the shared runners (429),
# so its images are looked up on Google's mirror first, which has the same digests; the build
# pulls them from there too (buildkitd-config-inline in runtime-images.yml).
declare -A digests
digest() {
	if [ -z "${digests[$1]+set}" ]; then
		local d="" first=${1%%/*}
		if [[ $1 != */* ]]; then
			d=$(inspect_digest "mirror.gcr.io/library/$1")
		elif [[ $first != *.* && $first != *:* ]]; then
			d=$(inspect_digest "mirror.gcr.io/$1")
		fi
		[ -n "$d" ] || d=$(inspect_digest "$1")
		digests[$1]=$d
	fi
	echo "${digests[$1]}"
}

# The labels and creation time of a published image (the first platform of a multi-arch one).
published() {
	docker buildx imagetools inspect "$1" --format '{{json .Image}}' 2>/dev/null |
		jq -c 'if has("config") then . else (to_entries[0].value // {}) end | {created: (.created // ""), bases: (.config.Labels["envoryx.base.digests"] // "")}' || true
}

now=$(date -u +%s)
include='[]'
while IFS=$'\t' read -r version base missing; do
	bases=() unresolved=0
	for tmpl in "$@"; do
		ref=${tmpl//\{base\}/$base}
		d=$(digest "$ref")
		if [ -z "$d" ]; then
			echo "::warning::could not resolve ${ref}; ${version} builds from whatever the tag points to if it is built" >&2
			unresolved=1
			continue
		fi
		bases+=("${ref}@${d}")
	done
	key="${bases[*]}"

	if [ "$mode" = changed ]; then
		pub=$(published "${image}:${version}")
		[ -n "$pub" ] || pub='{}'
		old_key=$(jq -r '.bases' <<<"$pub")
		created=$(jq -r '.created' <<<"$pub")
		age_days=99
		if [ -n "$created" ]; then
			age_days=$(((now - $(date -u -d "$created" +%s)) / 86400))
		fi
		if [ "$age_days" -lt 6 ] && { [ "$key" = "$old_key" ] || [ "$unresolved" = 1 ]; }; then
			echo "${version}: unchanged (built ${age_days} days ago from ${old_key:-unknown bases})" >&2
			continue
		fi
		echo "${version}: rebuilding (built ${age_days} days ago; bases ${old_key:-unknown} -> ${key})" >&2
	fi

	include=$(jq -c --arg v "$version" --arg b "$base" --arg m "$missing" --arg k "$key" \
		'. + [{version: $v, base: $b, missing: $m, bases: $k}]' <<<"$include")
done < <(jq -r '.versions[] | [.version, .base, ((.missingExtensions // []) | join(" "))] | @tsv' "$versions")

jq -c '{include: .}' <<<"$include"
