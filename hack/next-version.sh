#!/usr/bin/env bash
# Prints the next semantic version (no leading v) from Conventional Commits since the latest vX.Y.Z tag.
# Breaking (type! or a BREAKING CHANGE line) -> major, feat -> minor, anything else -> patch.
# When HEAD already carries the latest tag, prints that version unchanged.
set -euo pipefail

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)
range="HEAD"
[[ -n "$last" ]] && range="$last..HEAD"
if [[ -n "$last" && -z "$(git rev-list -n1 "$range")" ]]; then
  echo "${last#v}"
  exit 0
fi

breaking='^[a-zA-Z]+(\([^)]*\))?!:'
feature='^feat(\([^)]*\))?:'
bump=patch
while IFS= read -r -d '' msg; do
  msg=${msg#"${msg%%[!$'\n']*}"}
  subject=${msg%%$'\n'*}
  if [[ $subject =~ $breaking ]] || grep -qE '^BREAKING[ -]CHANGE' <<<"$msg"; then
    bump=major
    break
  fi
  if [[ $subject =~ $feature ]]; then
    bump=minor
  fi
done < <(git log --format='%B%x00' "$range")

IFS=. read -r major minor patch <<<"${last#v}"
major=${major:-0} minor=${minor:-0} patch=${patch:-0}
case $bump in
  major) echo "$((major + 1)).0.0" ;;
  minor) echo "$major.$((minor + 1)).0" ;;
  patch) echo "$major.$minor.$((patch + 1))" ;;
esac
