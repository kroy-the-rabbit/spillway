#!/usr/bin/env bash
# Upgrade-path check for the spillway Helm chart.
#
# 1. Installs the previously released chart from the OCI registry.
# 2. Creates an annotated Secret and waits for its replica.
# 3. Creates a spillway.kroy.io/v1alpha1 SpillwayProfile (the only version
#    the previous releases serve) and waits for its replica.
# 4. Upgrades the release in place to the local chart (charts/spillway) with
#    the locally built image.
# 5. Asserts the replica survived the upgrade and that the source is still
#    reconciled (edit source -> replica updates).
# 6. Asserts the profile stored as v1alpha1 is readable through the v1
#    endpoint, is still reconciled by the upgraded controller, and that
#    hack/migrate-storage-version.sh leaves the CRD storing only v1.
# 7. Uninstalls the release and asserts the Deployment is gone.
#
# Requirements: kubectl and helm on PATH; KUBECONFIG (or the default context)
# pointing at a cluster that already has the image ${IMAGE_REPOSITORY}:${IMAGE_TAG}
# available (e.g. via `kind load docker-image`).
#
# Environment:
#   PREVIOUS_CHART           OCI reference of the released chart
#                            (default oci://ghcr.io/kroy-the-rabbit/charts/spillway)
#   PREVIOUS_CHART_VERSION   released chart version to start from (default 0.6.0)
#   CHART_DIR                local chart to upgrade to (default charts/spillway)
#   IMAGE_REPOSITORY         image repository for the upgraded release (default spillway)
#   IMAGE_TAG                image tag for the upgraded release (default e2e)
#   RELEASE                  Helm release name (default spillway)
#   SPILLWAY_NAMESPACE       controller namespace (default spillway-system)
#   E2E_TIMEOUT              seconds to wait for each condition (default 60)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREVIOUS_CHART="${PREVIOUS_CHART:-oci://ghcr.io/kroy-the-rabbit/charts/spillway}"
PREVIOUS_CHART_VERSION="${PREVIOUS_CHART_VERSION:-0.6.0}"
CHART_DIR="${CHART_DIR:-${REPO_ROOT}/charts/spillway}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-spillway}"
IMAGE_TAG="${IMAGE_TAG:-e2e}"
RELEASE="${RELEASE:-spillway}"
NS="${SPILLWAY_NAMESPACE:-spillway-system}"
TIMEOUT="${E2E_TIMEOUT:-60}"

SRC_NS="upgrade-src"
DST_NS="upgrade-dst"
SECRET_NAME="upgrade-token"
PROFILE_NAME="upgrade-profile"
PROFILE_CM="upgrade-env"
CRD_NAME="spillwayprofiles.spillway.kroy.io"

failures=0
passes=0
pass() { passes=$((passes + 1)); echo "PASS: $*"; }
fail() { failures=$((failures + 1)); echo "FAIL: $*"; }

wait_until() {
  local desc="$1"; shift
  local deadline=$((SECONDS + TIMEOUT))
  while (( SECONDS < deadline )); do
    if "$@" >/dev/null 2>&1; then pass "$desc"; return 0; fi
    sleep 2
  done
  fail "$desc (timed out after ${TIMEOUT}s)"
}

assert_now() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "$desc"; else fail "$desc"; fi
}

secret_token_equals() {
  local actual
  actual="$(kubectl get secret -n "$1" "$2" -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null)" || return 1
  [[ "$actual" == "$3" ]]
}

deployment_image_is() {
  local actual
  actual="$(kubectl get deployment -n "$NS" "$RELEASE" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)" || return 1
  [[ "$actual" == "$1" ]]
}

configmap_value_equals() {
  local actual
  actual="$(kubectl get configmap -n "$1" "$2" -o jsonpath="{.data.$3}" 2>/dev/null)" || return 1
  [[ "$actual" == "$4" ]]
}

# crd_stored_versions_are <json-array>
# Compares status.storedVersions of the SpillwayProfile CRD verbatim.
crd_stored_versions_are() {
  local actual
  actual="$(kubectl get crd "$CRD_NAME" -o jsonpath='{.status.storedVersions}' 2>/dev/null)" || return 1
  [[ "$actual" == "$1" ]]
}

# crd_stored_versions_contain <version>
crd_stored_versions_contain() {
  kubectl get crd "$CRD_NAME" -o jsonpath='{.status.storedVersions}' 2>/dev/null | grep -q "\"$1\""
}

crd_storage_version_is() {
  local actual
  actual="$(kubectl get crd "$CRD_NAME" -o jsonpath='{.spec.versions[?(@.storage==true)].name}' 2>/dev/null)" || return 1
  [[ "$actual" == "$1" ]]
}

release_chart_is() {
  local actual
  actual="$(helm list -n "$NS" -o json | tr -d '\n' | sed -nE "s/.*\"name\":\"${RELEASE}\"[^}]*\"chart\":\"([^\"]+)\".*/\1/p")" || return 1
  [[ "$actual" == "$1" ]]
}

rollout_ok() {
  kubectl rollout status deployment/"$RELEASE" -n "$NS" --timeout=180s
}

cleanup() {
  echo "cleaning up"
  # Remove the source and profile first so the (still running) controller can
  # honour their finalizers and delete the replicas before it goes away.
  kubectl delete spillwayprofile -n "$SRC_NS" "$PROFILE_NAME" --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
  kubectl delete secret -n "$SRC_NS" "$SECRET_NAME" --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
  kubectl delete configmap -n "$SRC_NS" "$PROFILE_CM" --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
  kubectl delete namespace "$SRC_NS" "$DST_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== install previous release ${PREVIOUS_CHART}:${PREVIOUS_CHART_VERSION}"
helm install "$RELEASE" "$PREVIOUS_CHART" \
  --version "$PREVIOUS_CHART_VERSION" \
  --namespace "$NS" \
  --create-namespace \
  --set metrics.serviceMonitor.enabled=false \
  --wait --timeout 5m >/dev/null
assert_now "release ${RELEASE} reports chart spillway-${PREVIOUS_CHART_VERSION}" \
  release_chart_is "spillway-${PREVIOUS_CHART_VERSION}"
assert_now "previous release rolled out" rollout_ok

echo "== replicate a Secret with the previous release"
kubectl create namespace "$SRC_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create namespace "$DST_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${SRC_NS}
  annotations:
    spillway.kroy.io/replicate-to: "${DST_NS}"
type: Opaque
stringData:
  token: before-upgrade
YAML
wait_until "replica created in ${DST_NS} by ${PREVIOUS_CHART_VERSION}" \
  secret_token_equals "$DST_NS" "$SECRET_NAME" before-upgrade
replica_uid_before="$(kubectl get secret -n "$DST_NS" "$SECRET_NAME" -o jsonpath='{.metadata.uid}')"

echo "== create a v1alpha1 SpillwayProfile with the previous release"
# Releases before 1.0 only serve spillway.kroy.io/v1alpha1, so this is the
# version every pre-upgrade profile is persisted as.
assert_now "previous release stores SpillwayProfile as v1alpha1" \
  crd_storage_version_is v1alpha1
kubectl create configmap "$PROFILE_CM" --namespace "$SRC_NS" \
  --from-literal=LOG_LEVEL=before-upgrade \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f - >/dev/null <<YAML
apiVersion: spillway.kroy.io/v1alpha1
kind: SpillwayProfile
metadata:
  name: ${PROFILE_NAME}
  namespace: ${SRC_NS}
spec:
  targetNamespaces:
    - ${DST_NS}
  sources:
    - kind: ConfigMap
      name: ${PROFILE_CM}
YAML
wait_until "profile replica created in ${DST_NS} by ${PREVIOUS_CHART_VERSION}" \
  configmap_value_equals "$DST_NS" "$PROFILE_CM" LOG_LEVEL before-upgrade
profile_uid_before="$(kubectl get spillwayprofile -n "$SRC_NS" "$PROFILE_NAME" -o jsonpath='{.metadata.uid}')"

echo "== upgrade to local chart ${CHART_DIR} with image ${IMAGE_REPOSITORY}:${IMAGE_TAG}"
helm upgrade "$RELEASE" "$CHART_DIR" \
  --namespace "$NS" \
  --set image.repository="$IMAGE_REPOSITORY" \
  --set image.tag="$IMAGE_TAG" \
  --set image.pullPolicy=IfNotPresent \
  --set metrics.serviceMonitor.enabled=false \
  --wait --timeout 5m >/dev/null
local_chart_version="$(sed -nE 's/^version:[[:space:]]*"?([^"]+)"?$/\1/p' "${CHART_DIR}/Chart.yaml")"
assert_now "release ${RELEASE} reports chart spillway-${local_chart_version}" \
  release_chart_is "spillway-${local_chart_version}"
assert_now "upgraded release rolled out" rollout_ok
assert_now "deployment runs ${IMAGE_REPOSITORY}:${IMAGE_TAG}" \
  deployment_image_is "${IMAGE_REPOSITORY}:${IMAGE_TAG}"
wait_until "all pods run the upgraded image" bash -c \
  "test \"\$(kubectl get pods -n '$NS' -l app.kubernetes.io/name=spillway -o jsonpath='{range .items[*]}{.spec.containers[0].image}{\"\\n\"}{end}' | sort -u)\" = '${IMAGE_REPOSITORY}:${IMAGE_TAG}'"

echo "== replica survives the upgrade"
assert_now "replica still present in ${DST_NS} with pre-upgrade data" \
  secret_token_equals "$DST_NS" "$SECRET_NAME" before-upgrade
replica_uid_after="$(kubectl get secret -n "$DST_NS" "$SECRET_NAME" -o jsonpath='{.metadata.uid}')"
assert_now "replica object was not recreated during the upgrade (same uid)" \
  test "$replica_uid_before" = "$replica_uid_after"

echo "== source is still reconciled after the upgrade"
kubectl patch secret -n "$SRC_NS" "$SECRET_NAME" --type merge \
  -p '{"stringData":{"token":"after-upgrade"}}' >/dev/null
wait_until "replica in ${DST_NS} updated by the upgraded controller" \
  secret_token_equals "$DST_NS" "$SECRET_NAME" after-upgrade

echo "== profile stored as v1alpha1 is served as v1 after the upgrade"
assert_now "CRD storage version is v1 after the upgrade" \
  crd_storage_version_is v1
assert_now "CRD still serves v1alpha1" \
  bash -c "kubectl get crd '$CRD_NAME' -o jsonpath='{.spec.versions[?(@.name==\"v1alpha1\")].served}' | grep -qx true"
assert_now "CRD storedVersions still lists v1alpha1 before migration" \
  crd_stored_versions_contain v1alpha1
assert_now "CRD storedVersions lists v1 after the CRD upgrade" \
  crd_stored_versions_contain v1
assert_now "profile readable via spillwayprofiles.v1.spillway.kroy.io" \
  kubectl get spillwayprofiles.v1.spillway.kroy.io -n "$SRC_NS" "$PROFILE_NAME"
assert_now "profile still readable via spillwayprofiles.v1alpha1.spillway.kroy.io" \
  kubectl get spillwayprofiles.v1alpha1.spillway.kroy.io -n "$SRC_NS" "$PROFILE_NAME"
profile_uid_after="$(kubectl get spillwayprofiles.v1.spillway.kroy.io -n "$SRC_NS" "$PROFILE_NAME" -o jsonpath='{.metadata.uid}')"
assert_now "profile is the same object under v1 (same uid)" \
  test "$profile_uid_before" = "$profile_uid_after"
assert_now "profile replica still present in ${DST_NS} with pre-upgrade data" \
  configmap_value_equals "$DST_NS" "$PROFILE_CM" LOG_LEVEL before-upgrade
kubectl patch configmap -n "$SRC_NS" "$PROFILE_CM" --type merge \
  -p '{"data":{"LOG_LEVEL":"after-upgrade"}}' >/dev/null
wait_until "profile replica in ${DST_NS} updated by the upgraded controller" \
  configmap_value_equals "$DST_NS" "$PROFILE_CM" LOG_LEVEL after-upgrade

echo "== migrate stored objects to v1"
# Run the migration visibly so its per-object log lands in the e2e output.
if "${REPO_ROOT}/hack/migrate-storage-version.sh"; then
  pass "hack/migrate-storage-version.sh succeeds"
else
  fail "hack/migrate-storage-version.sh succeeds"
fi
assert_now "CRD storedVersions is [\"v1\"] after migration" \
  crd_stored_versions_are '["v1"]'
if "${REPO_ROOT}/hack/migrate-storage-version.sh" >/dev/null; then
  pass "hack/migrate-storage-version.sh is idempotent"
else
  fail "hack/migrate-storage-version.sh is idempotent"
fi
assert_now "CRD storedVersions still [\"v1\"] after a second run" \
  crd_stored_versions_are '["v1"]'
assert_now "profile survived the migration (same uid)" \
  test "$profile_uid_before" = "$(kubectl get spillwayprofile -n "$SRC_NS" "$PROFILE_NAME" -o jsonpath='{.metadata.uid}')"
kubectl patch configmap -n "$SRC_NS" "$PROFILE_CM" --type merge \
  -p '{"data":{"LOG_LEVEL":"after-migration"}}' >/dev/null
wait_until "profile replica in ${DST_NS} updated after the migration" \
  configmap_value_equals "$DST_NS" "$PROFILE_CM" LOG_LEVEL after-migration

echo "== uninstall"
cleanup
trap - EXIT
wait_until "replica removed from ${DST_NS} after source deletion" \
  bash -c "! kubectl get secret -n '$DST_NS' '$SECRET_NAME'"
wait_until "profile replica removed from ${DST_NS} after profile deletion" \
  bash -c "! kubectl get configmap -n '$DST_NS' '$PROFILE_CM'"
# The chart owns its namespace, so uninstall also deletes the namespace that
# holds the release record and helm may report "release: not found" while
# purging it. Removal is verified explicitly below instead of via exit code.
helm uninstall "$RELEASE" --namespace "$NS" --wait --timeout 5m >/dev/null 2>&1 || true
wait_until "deployment ${RELEASE} is gone after helm uninstall" \
  bash -c "! kubectl get deployment -n '$NS' '$RELEASE'"
# The chart owns the namespace, so uninstall deletes it; wait for it to finish
# terminating so a following fresh install does not race the deletion.
wait_until "namespace ${NS} removed after helm uninstall" \
  bash -c "! kubectl get namespace '$NS'"
assert_now "helm release ${RELEASE} no longer listed" \
  bash -c "! helm status '$RELEASE' --namespace '$NS'"

echo
echo "== summary: ${passes} passed, ${failures} failed"
if (( failures > 0 )); then
  echo "UPGRADE CHECK FAILED"
  exit 1
fi
echo "UPGRADE CHECK PASSED"
