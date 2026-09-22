#!/usr/bin/env bash
# End-to-end checks for a running spillway controller.
#
# Requirements: kubectl on PATH pointing at a cluster (via KUBECONFIG or the
# default context) where spillway is installed and its CRD is present.
#
# Environment:
#   E2E_TIMEOUT   seconds to wait for each condition (default 60)
#   E2E_KEEP      set to 1 to leave test objects in place on exit
#
# Every check prints "PASS: <desc>" or "FAIL: <desc>"; the script exits
# non-zero if any check failed.
set -euo pipefail

TIMEOUT="${E2E_TIMEOUT:-60}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ANN_REPLICATE_TO="spillway.kroy.io/replicate-to"
ANN_EXCLUDE_NS="spillway.kroy.io/exclude-namespaces"
ANN_MANAGED_BY="spillway.kroy.io/managed-by"
ANN_SOURCE_FROM="spillway.kroy.io/source-from"
ANN_PROFILE_REF="spillway.kroy.io/profile-ref"

SOURCE_NS="platform"
TARGET_NSS=(team-a team-b)
EXCLUDED_NS="other"
ALL_NSS=("$SOURCE_NS" "${TARGET_NSS[@]}" "$EXCLUDED_NS")

SECRET_NAME="e2e-token"
CM_NAME="e2e-config"
PROFILE_NAME="platform-defaults"        # from examples/profile-basic.yaml
PROFILE_SECRET="registry-pull-secret"   # prerequisite named in the example
PROFILE_CM="shared-env"                 # prerequisite named in the example

failures=0
passes=0

pass() { passes=$((passes + 1)); echo "PASS: $*"; }
fail() { failures=$((failures + 1)); echo "FAIL: $*"; }

# wait_until <description> <command...>
# Polls until the command exits 0 (stdout/stderr discarded) or TIMEOUT elapses.
wait_until() {
  local desc="$1"; shift
  local deadline=$((SECONDS + TIMEOUT))
  while (( SECONDS < deadline )); do
    if "$@" >/dev/null 2>&1; then
      pass "$desc"
      return 0
    fi
    sleep 2
  done
  fail "$desc (timed out after ${TIMEOUT}s)"
  return 0
}

# assert_now <description> <command...>
# Single-shot check with no polling.
assert_now() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    pass "$desc"
  else
    fail "$desc"
  fi
}

exists() { kubectl get "$1" -n "$2" "$3" >/dev/null 2>&1; }
absent() { ! exists "$@"; }

# field <kind> <ns> <name> <jsonpath>
field() { kubectl get "$1" -n "$2" "$3" -o jsonpath="$4" 2>/dev/null; }

# data_equals <kind> <ns> <name> <key> <expected-plaintext>
# Secrets are compared after base64-decoding; ConfigMaps as-is.
data_equals() {
  local kind="$1" ns="$2" name="$3" key="$4" expected="$5" actual
  actual="$(field "$kind" "$ns" "$name" "{.data.$key}")" || return 1
  if [[ "$kind" == "secret" ]]; then
    actual="$(printf '%s' "$actual" | base64 -d 2>/dev/null)" || return 1
  fi
  [[ "$actual" == "$expected" ]]
}

# has_key / lacks_key <kind> <ns> <name> <key>
has_key()   { [[ -n "$(field "$1" "$2" "$3" "{.data.$4}")" ]]; }
lacks_key() { exists "$1" "$2" "$3" && [[ -z "$(field "$1" "$2" "$3" "{.data.$4}")" ]]; }

# annotation_equals <kind> <ns> <name> <annotation> <expected>
annotation_equals() {
  local actual
  actual="$(field "$1" "$2" "$3" "{.metadata.annotations.$(printf '%s' "$4" | sed 's/\./\\./g')}")" || return 1
  [[ "$actual" == "$5" ]]
}

ensure_namespace() {
  kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

cleanup() {
  if [[ "${E2E_KEEP:-0}" == "1" ]]; then
    echo "E2E_KEEP=1: leaving test objects in place"
    return
  fi
  echo "cleaning up test objects"
  # Sources and profiles carry a finalizer that the controller clears after
  # removing their replicas, so wait for the deletes while it is still running.
  kubectl delete spillwayprofile -n "$SOURCE_NS" "$PROFILE_NAME" --ignore-not-found --timeout="${TIMEOUT}s" >/dev/null 2>&1 || true
  kubectl delete secret -n "$SOURCE_NS" "$SECRET_NAME" "$PROFILE_SECRET" --ignore-not-found --timeout="${TIMEOUT}s" >/dev/null 2>&1 || true
  kubectl delete configmap -n "$SOURCE_NS" "$CM_NAME" "$PROFILE_CM" --ignore-not-found --timeout="${TIMEOUT}s" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
echo "== preflight"
kubectl version >/dev/null
assert_now "SpillwayProfile CRD is installed" \
  kubectl get crd spillwayprofiles.spillway.kroy.io

for ns in "${ALL_NSS[@]}"; do
  ensure_namespace "$ns"
done
pass "namespaces exist: ${ALL_NSS[*]}"

# Start from a clean slate so the script can be re-run locally.
cleanup
for ns in "${TARGET_NSS[@]}" "$EXCLUDED_NS"; do
  wait_until "no leftover secret $SECRET_NAME in $ns" absent secret "$ns" "$SECRET_NAME"
done
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Annotation-driven replication
# ---------------------------------------------------------------------------
echo "== annotation-driven replication"

kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${SOURCE_NS}
  annotations:
    ${ANN_REPLICATE_TO}: "team-*"
type: Opaque
stringData:
  token: alpha
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${CM_NAME}
  namespace: ${SOURCE_NS}
  annotations:
    ${ANN_REPLICATE_TO}: "all"
    ${ANN_EXCLUDE_NS}: "${EXCLUDED_NS}"
data:
  LOG_LEVEL: info
YAML

for ns in "${TARGET_NSS[@]}"; do
  wait_until "Secret replica appears in $ns with matching data" \
    data_equals secret "$ns" "$SECRET_NAME" token alpha
  assert_now "Secret replica in $ns is annotated ${ANN_MANAGED_BY}=spillway" \
    annotation_equals secret "$ns" "$SECRET_NAME" "$ANN_MANAGED_BY" spillway
  assert_now "Secret replica in $ns is annotated ${ANN_SOURCE_FROM}=Secret/${SOURCE_NS}/${SECRET_NAME}" \
    annotation_equals secret "$ns" "$SECRET_NAME" "$ANN_SOURCE_FROM" "Secret/${SOURCE_NS}/${SECRET_NAME}"
  wait_until "ConfigMap replica appears in $ns with matching data" \
    data_equals configmap "$ns" "$CM_NAME" LOG_LEVEL info
done

# The controller has demonstrably processed both sources by now (replicas exist
# in the targets), so a negative check against the excluded namespace is sound.
assert_now "Secret does NOT appear in $EXCLUDED_NS (glob team-* does not match)" \
  absent secret "$EXCLUDED_NS" "$SECRET_NAME"
assert_now "ConfigMap does NOT appear in $EXCLUDED_NS (excluded via ${ANN_EXCLUDE_NS})" \
  absent configmap "$EXCLUDED_NS" "$CM_NAME"
assert_now "Secret does NOT appear in kube-system (protected namespace)" \
  absent secret kube-system "$SECRET_NAME"
assert_now "ConfigMap does NOT appear in kube-system (protected namespace)" \
  absent configmap kube-system "$CM_NAME"

echo "== source update propagates"
kubectl patch secret -n "$SOURCE_NS" "$SECRET_NAME" --type merge \
  -p "{\"stringData\":{\"token\":\"beta\"}}" >/dev/null
for ns in "${TARGET_NSS[@]}"; do
  wait_until "Secret replica in $ns updated to new data" \
    data_equals secret "$ns" "$SECRET_NAME" token beta
done

echo "== controller records events on the source"
source_has_event() {
  kubectl get events -n "$SOURCE_NS" \
    --field-selector "involvedObject.name=$SECRET_NAME,reason=ReplicationSucceeded" \
    -o name 2>/dev/null | grep -q .
}
wait_until "ReplicationSucceeded event recorded on source Secret (events.k8s.io RBAC)" \
  source_has_event

echo "== deleted replica is recreated"
kubectl delete secret -n "${TARGET_NSS[0]}" "$SECRET_NAME" >/dev/null
wait_until "Secret replica in ${TARGET_NSS[0]} recreated after deletion with current data" \
  data_equals secret "${TARGET_NSS[0]}" "$SECRET_NAME" token beta

echo "== source deletion removes replicas"
kubectl delete secret -n "$SOURCE_NS" "$SECRET_NAME" >/dev/null
for ns in "${TARGET_NSS[@]}"; do
  wait_until "Secret replica removed from $ns after source deletion" \
    absent secret "$ns" "$SECRET_NAME"
done

# ---------------------------------------------------------------------------
# SpillwayProfile (examples/profile-basic.yaml)
# ---------------------------------------------------------------------------
echo "== SpillwayProfile replication"

# The example lists these as prerequisites in the "platform" namespace. Note
# that the sources carry no spillway annotations at all.
kubectl create secret generic "$PROFILE_SECRET" --namespace "$SOURCE_NS" \
  --from-literal=.dockerconfigjson='{"auths":{}}' \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create configmap "$PROFILE_CM" --namespace "$SOURCE_NS" \
  --from-literal=LOG_LEVEL=info \
  --from-literal=SENTRY_DSN=https://example.invalid/1 \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl apply -f "${REPO_ROOT}/examples/profile-basic.yaml" >/dev/null

for ns in "${TARGET_NSS[@]}"; do
  wait_until "profile Secret replica $PROFILE_SECRET appears in $ns" \
    data_equals secret "$ns" "$PROFILE_SECRET" '\.dockerconfigjson' '{"auths":{}}'
  assert_now "profile Secret replica in $ns is annotated ${ANN_PROFILE_REF}=${SOURCE_NS}/${PROFILE_NAME}" \
    annotation_equals secret "$ns" "$PROFILE_SECRET" "$ANN_PROFILE_REF" "${SOURCE_NS}/${PROFILE_NAME}"
  wait_until "profile ConfigMap replica $PROFILE_CM appears in $ns with LOG_LEVEL" \
    data_equals configmap "$ns" "$PROFILE_CM" LOG_LEVEL info
  assert_now "profile ConfigMap replica in $ns excludes SENTRY_DSN (excludeKeys)" \
    lacks_key configmap "$ns" "$PROFILE_CM" SENTRY_DSN
done
assert_now "profile Secret does NOT appear in $EXCLUDED_NS (not matched by targetNamespaces)" \
  absent secret "$EXCLUDED_NS" "$PROFILE_SECRET"
wait_until "profile status lists ${TARGET_NSS[*]} as replicated namespaces" \
  bash -c "kubectl get spillwayprofile -n '$SOURCE_NS' '$PROFILE_NAME' -o jsonpath='{.status.replicatedNamespaces}' | grep -q team-a && \
           kubectl get spillwayprofile -n '$SOURCE_NS' '$PROFILE_NAME' -o jsonpath='{.status.replicatedNamespaces}' | grep -q team-b"

echo "== SpillwayProfile deletion removes its replicas"
kubectl delete spillwayprofile -n "$SOURCE_NS" "$PROFILE_NAME" >/dev/null
for ns in "${TARGET_NSS[@]}"; do
  wait_until "profile Secret replica removed from $ns after profile deletion" \
    absent secret "$ns" "$PROFILE_SECRET"
  wait_until "profile ConfigMap replica removed from $ns after profile deletion" \
    absent configmap "$ns" "$PROFILE_CM"
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo
echo "== summary: ${passes} passed, ${failures} failed"
if (( failures > 0 )); then
  echo "E2E FAILED"
  exit 1
fi
echo "E2E PASSED"
