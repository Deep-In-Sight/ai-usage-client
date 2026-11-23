#!/bin/sh
# Publish DIR's four release files as the GitHub Release for TAG with gh.
# DRY_RUN=1 prints the command and the notes instead (and, in Actions, an
# annotation). Otherwise GH_TOKEN is needed and an existing release is never
# touched.
# Usage: tools/publish-release.sh DIR TAG
set -eu
dir="$1"
tag="$2"
fail() { echo "publish-release: $*" >&2; exit 1; }
printf '%s\n' "$tag" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?' || fail "bad tag: $tag"
names="claude-usage-linux-amd64 claude-usage-macos-universal claude-usage-windows-amd64.exe"
for n in $names SHA256SUMS; do
  [ -f "$dir/$n" ] || fail "$dir/$n is missing"
done
count=$(find "$dir" -mindepth 1 -maxdepth 1 | wc -l)
[ "$count" -eq 4 ] || fail "$dir must hold exactly the four release files (it holds $count)"
listed=$(awk '{ print $2 }' "$dir/SHA256SUMS" | sort | tr '\n' ' ')
[ "$listed" = "$(printf '%s\n' $names | sort | tr '\n' ' ')" ] || fail "SHA256SUMS must name exactly the three files"
grep -Evq '^[0-9a-f]{64}  claude-usage-[a-z0-9.-]+$' "$dir/SHA256SUMS" && fail "SHA256SUMS has a malformed line"
(cd "$dir" && sha256sum --quiet --strict -c SHA256SUMS) || fail "a file does not match SHA256SUMS"

notes=$(mktemp)
trap 'rm -f "$notes"' EXIT
{
  echo "Claude Usage agent $tag."
  echo
  echo "Install it from your team's Claude Usage server, not from this page: the server's install command writes the settings the agent needs."
  echo
  echo "SHA-256:"
  cat "$dir/SHA256SUMS"
  echo
  echo "Built by ${GITHUB_SERVER_URL:-?}/${GITHUB_REPOSITORY:-?}/actions/runs/${GITHUB_RUN_ID:-?} from commit ${GITHUB_SHA:-?}."
} > "$notes"

pre=""
case "$tag" in *-*) pre="--prerelease" ;; esac
files="$dir/claude-usage-linux-amd64 $dir/claude-usage-windows-amd64.exe $dir/claude-usage-macos-universal $dir/SHA256SUMS"
# A draft first: it is published only once all four files are attached.
shown="gh release create $tag --verify-tag --draft --title \"Claude Usage $tag\" --notes-file $notes${pre:+ $pre} $files"
shown2="gh release edit $tag --draft=false"

if [ "${DRY_RUN:-}" = "1" ]; then
  echo "DRY RUN: $shown"
  echo "DRY RUN: (check the four attached files) then $shown2"
  echo "DRY RUN: $shown2"
  cat "$notes"
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::notice title=release dry run::$shown && $shown2"
  fi
  exit 0
fi
[ -n "${GH_TOKEN:-}" ] || fail "GH_TOKEN is not set"
if gh release view "$tag" >/dev/null 2>&1; then
  fail "release $tag already exists (maybe a draft from a failed run); it is left as it is"
fi
# shellcheck disable=SC2086
gh release create "$tag" --verify-tag --draft --title "Claude Usage $tag" --notes-file "$notes" $pre $files
want=$(for f in $files; do printf '%s %s\n' "$(basename "$f")" "$(wc -c < "$f" | tr -d ' ')"; done | sort)
got=$(gh release view "$tag" --json assets --jq '.assets[] | "\(.name) \(.size)"' | sort)
[ "$got" = "$want" ] || fail "the draft release $tag does not hold the four files as built; it stays a draft"
gh release edit "$tag" --draft=false
