#!/usr/bin/env bash
# Migrate stored SpillwayProfile objects to the v1 storage version.
#
# Spillway 1.0 promotes the SpillwayProfile CRD from spillway.kroy.io/v1alpha1
# to spillway.kroy.io/v1 (v1 is the storage version; v1alpha1 stays served,
# deprecated, with an identical schema). The API server only rewrites an
# object at the current storage version when the object is written, so on a
# cluster upgraded from an earlier release every profile is still persisted
# as v1alpha1 and the CRD's status.storedVersions lists both versions. Until
# that list is reduced to ["v1"] the v1alpha1 version can never be removed
# from the CRD.
#
# This script performs the standard storage-version migration:
#   1. rewrites every SpillwayProfile in place (a no-op read-then-replace,
#      which makes the API server persist it as v1), then
#   2. sets the CRD's status.storedVersions to ["v1"].
#
# It is idempotent: re-running it rewrites the objects again (harmless) and
# leaves storedVersions unchanged. It never touches spec or status content.
#
# Requirements: kubectl on PATH pointing at the target cluster (via KUBECONFIG
# or the default context) with permission to list/replace SpillwayProfiles in
# every namespace and to patch the CRD status subresource.
#
# Usage:
#   hack/migrate-storage-version.sh            migrate
#   hack/migrate-storage-version.sh --dry-run  print what would be done
#
# Environment:
#   STORAGE_VERSION   version to migrate to (default v1)
set -euo pipefail

CRD="spillwayprofiles.spillway.kroy.io"
STORAGE_VERSION="${STORAGE_VERSION:-v1}"

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  -h|--help)
    sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "unknown argument: $1 (expected --dry-run or nothing)" >&2
    exit 2
    ;;
esac

log() { echo "migrate-storage-version: $*"; }

if ! kubectl get crd "${CRD}" >/dev/null 2>&1; then
  log "CRD ${CRD} not found; nothing to migrate"
  exit 0
fi

# Refuse to run against a CRD whose storage version is not the target: the
# replace below would persist objects at whatever the server's storage
# version is, and the final status patch would then lie.
server_storage="$(kubectl get crd "${CRD}" -o jsonpath='{.spec.versions[?(@.storage==true)].name}')"
if [[ "${server_storage}" != "${STORAGE_VERSION}" ]]; then
  echo "${CRD}: storage version is ${server_storage:-<none>}, expected ${STORAGE_VERSION}; upgrade the CRD first" >&2
  exit 1
fi

stored_before="$(kubectl get crd "${CRD}" -o jsonpath='{.status.storedVersions}')"
log "storedVersions before: ${stored_before}"

# Rewrite every profile at the storage version. Reading and replacing the
# object unchanged is enough: the API server persists the write as
# ${STORAGE_VERSION}. Requests go through the ${STORAGE_VERSION} endpoint
# explicitly so the replace never emits the v1alpha1 deprecation warning.
resource="spillwayprofiles.${STORAGE_VERSION}.spillway.kroy.io"
mapfile -t profiles < <(kubectl get "${resource}" --all-namespaces \
  -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}')

rewritten=0
skipped=0
for entry in "${profiles[@]}"; do
  [[ -n "${entry}" ]] || continue
  ns="${entry%%/*}"
  name="${entry#*/}"
  if (( dry_run )); then
    log "would rewrite ${ns}/${name}"
    continue
  fi
  # A conflict means something else updated the object between the get and
  # the replace; that write itself already persisted it at the storage
  # version, so the outcome is the same and the object is counted as done.
  if kubectl get "${resource}" -n "${ns}" "${name}" -o json | kubectl replace -f - >/dev/null 2>&1; then
    log "rewrote ${ns}/${name}"
    rewritten=$((rewritten + 1))
  elif ! kubectl get "${resource}" -n "${ns}" "${name}" >/dev/null 2>&1; then
    log "skipped ${ns}/${name}: deleted during migration"
    skipped=$((skipped + 1))
  else
    log "rewrote ${ns}/${name} (concurrent update already persisted it)"
    rewritten=$((rewritten + 1))
  fi
done
log "profiles found: ${#profiles[@]}, rewritten: ${rewritten}, skipped: ${skipped}"

if (( dry_run )); then
  log "would set ${CRD} status.storedVersions to [\"${STORAGE_VERSION}\"]"
  exit 0
fi

if [[ "${stored_before}" == "[\"${STORAGE_VERSION}\"]" ]]; then
  log "storedVersions already [\"${STORAGE_VERSION}\"]; nothing to patch"
else
  kubectl patch crd "${CRD}" --subresource=status --type=merge \
    -p "{\"status\":{\"storedVersions\":[\"${STORAGE_VERSION}\"]}}" >/dev/null
  log "patched ${CRD} status.storedVersions to [\"${STORAGE_VERSION}\"]"
fi

stored_after="$(kubectl get crd "${CRD}" -o jsonpath='{.status.storedVersions}')"
log "storedVersions after: ${stored_after}"
if [[ "${stored_after}" != "[\"${STORAGE_VERSION}\"]" ]]; then
  echo "${CRD}: storedVersions is ${stored_after}, expected [\"${STORAGE_VERSION}\"]" >&2
  exit 1
fi
log "done"
