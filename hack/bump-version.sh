#!/usr/bin/env bash
# Single writer for the release version.
#
#   hack/bump-version.sh X.Y.Z    stamp X.Y.Z everywhere the version appears
#   hack/bump-version.sh --check  fail if any reference disagrees with Chart.yaml
#
# Chart.yaml `version` is the source of truth. Every other site is derived:
#   - Chart.yaml appVersion
#   - config/default/kustomization.yaml newTag
#   - README.md and docs/index.html (every bare X.Y.Z; v-prefixed examples
#     such as `v0.3.0` in prose are ignored)
#   - CHANGELOG.md: the [Unreleased] section is rolled into a new
#     [X.Y.Z] - YYYY-MM-DD section and the compare links are updated
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

CHART=charts/spillway/Chart.yaml
KUSTOMIZATION=config/default/kustomization.yaml
PROSE=(README.md docs/index.html)
CHANGELOG=CHANGELOG.md
REPO_URL=https://github.com/kroy-the-rabbit/spillway
SEMVER='[0-9]+\.[0-9]+\.[0-9]+'

chart_version() { awk '/^version:/{print $2}' "$CHART"; }
app_version()   { awk -F'"' '/^appVersion:/{print $2}' "$CHART"; }
kustomize_tag() { awk '/newTag:/{print $2}' "$KUSTOMIZATION"; }

check() {
  local want; want="$(chart_version)"
  local rc=0
  if [[ "$(app_version)" != "$want" ]]; then
    echo "MISMATCH $CHART appVersion=$(app_version) want=$want"; rc=1
  fi
  if [[ "$(kustomize_tag)" != "$want" ]]; then
    echo "MISMATCH $KUSTOMIZATION newTag=$(kustomize_tag) want=$want"; rc=1
  fi
  local f hits
  for f in "${PROSE[@]}"; do
    # bare X.Y.Z not preceded by 'v' or a digit/dot, not followed by digit/dot
    hits="$(grep -noE "(^|[^v0-9.])${SEMVER}([^0-9.]|$)" "$f" | grep -oE "${SEMVER}" | sort -u || true)"
    if [[ -z "$hits" ]]; then
      echo "MISMATCH $f contains no version reference (expected $want)"; rc=1
      continue
    fi
    while read -r v; do
      [[ "$v" == "$want" ]] || { echo "MISMATCH $f references $v want=$want"; rc=1; }
    done <<<"$hits"
  done
  if ! grep -qE "^## \[${want//./\\.}\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$" "$CHANGELOG"; then
    echo "MISMATCH $CHANGELOG has no '## [$want] - YYYY-MM-DD' section"; rc=1
  fi
  if [[ $rc -eq 0 ]]; then
    echo "OK: all version references are $want"
  fi
  return $rc
}

roll_changelog() {
  local old="$1" new="$2" today; today="$(date +%F)"
  grep -q '^## \[Unreleased\]$' "$CHANGELOG" || { echo "$CHANGELOG: missing '## [Unreleased]'" >&2; return 1; }
  # Warn if nothing has been recorded since the last release.
  if ! sed -n '/^## \[Unreleased\]$/,/^## \[/p' "$CHANGELOG" | grep -qE '^(###|- )'; then
    echo "warning: $CHANGELOG [Unreleased] section is empty" >&2
  fi
  sed -i -E "s|^## \[Unreleased\]$|## [Unreleased]\n\n## [${new}] - ${today}|" "$CHANGELOG"
  sed -i -E "s|^\[Unreleased\]: .*|[Unreleased]: ${REPO_URL}/compare/v${new}...HEAD\n[${new}]: ${REPO_URL}/compare/v${old}...v${new}|" "$CHANGELOG"
}

bump() {
  local new="$1"
  [[ "$new" =~ ^${SEMVER}(-[0-9A-Za-z.-]+)?$ ]] || { echo "not a version: $new" >&2; exit 2; }
  local old; old="$(chart_version)"
  [[ "$old" != "$new" ]] || { echo "already at $new"; return 0; }
  sed -i -E "s/^version: .*/version: ${new}/; s/^appVersion: .*/appVersion: \"${new}\"/" "$CHART"
  sed -i -E "s/(newTag:) .*/\1 ${new}/" "$KUSTOMIZATION"
  local f
  for f in "${PROSE[@]}"; do
    sed -i -E "s/(^|[^v0-9.])${old//./\\.}([^0-9.]|$)/\1${new}\2/g" "$f"
  done
  roll_changelog "$old" "$new"
  echo "bumped $old -> $new"
  check
}

case "${1:-}" in
  --check) check ;;
  "" | -h | --help) sed -n '2,12p' "$0"; exit 2 ;;
  *) bump "$1" ;;
esac
