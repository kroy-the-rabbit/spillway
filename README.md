# spillway

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/spillway)](https://artifacthub.io/packages/search?repo=spillway)

`spillway` replicates Kubernetes `Secrets` and `ConfigMaps` across namespaces in near real time. Two modes:

- **Annotation-driven** — add a single annotation to any existing source object.
- **SpillwayProfile** — a CRD for declarative platform bootstrap with no source annotations required.

## Project links

- Website (GitHub Pages): https://spillway.kroy.io
- Source: https://github.com/kroy-the-rabbit/spillway
- Helm chart (OCI): `oci://ghcr.io/kroy-the-rabbit/charts/spillway`
- Container image: `ghcr.io/kroy-the-rabbit/spillway`

The website is published from the `docs/` directory by `.github/workflows/deploy-pages.yaml` on pushes to `main`.

## How it works

- Add replication annotations to a source `Secret` or `ConfigMap`, or create a `SpillwayProfile` CRD.
- Spillway reconciles same-name replicas in target namespaces.
- Removing replication annotations (or deleting the profile) triggers cleanup of now-out-of-scope replicas.
- Deleting a source object triggers finalizer-based replica cleanup.
- Spillway watches namespace create events and label changes so namespace targeting updates promptly.

## Annotation contract

### Target selection

- `spillway.kroy.io/replicate-to`
  - Comma-separated namespace targets
  - Supports `all` or `*`, globs like `team-*`, explicit names like `payments`, or mixed forms
- `spillway.kroy.io/replicate-to-matching`
  - Kubernetes label selector for namespace labels (for example `env=prod` or `tier=frontend,region=us`)
  - Can be used together with `replicate-to`
- `spillway.kroy.io/exclude-namespaces`
  - Optional comma-separated exclusions, same syntax as `replicate-to`
  - Always takes precedence over include targeting

### Ownership and adoption

- `spillway.kroy.io/force-adopt: "true"`
  - Allows overwriting pre-existing unmanaged target objects and taking ownership
  - Default behavior is to skip conflicting unmanaged objects

### Key projection

- `spillway.kroy.io/include-keys: "key1,key2"`
  - Whitelist: only the listed data keys are copied into replicas
  - Mutually exclusive with `exclude-keys`; `include-keys` takes precedence
- `spillway.kroy.io/exclude-keys: "key1,key2"`
  - Blacklist: the listed data keys are omitted from replicas

### Replica TTL

- `spillway.kroy.io/replica-ttl: "24h"`
  - Go duration (e.g. `24h`, `168h`, `30m`). Replicas are stamped with an `expires-at` annotation at creation time.
  - When the TTL elapses the replica is **permanently deleted** — it is **not recreated**. The expired namespace is recorded in `spillway.kroy.io/expired-namespaces` on the source object and skipped on all future reconciles.
  - To re-enable replication to an expired namespace: manually remove that namespace from the `expired-namespaces` annotation value, or delete the annotation entirely.
  - Removing `replica-ttl` from a source that has `expired-namespaces` recorded automatically clears the expired record and resumes replication on the next reconcile.

### Namespace consent

- `spillway.kroy.io/accept-from` on the **target Namespace** (not the source object)
  - Absent → accept from all sources (backward-compatible default)
  - `"all"` or `"*"` → accept from all sources
  - `"Secret/platform/*"` → any Secret from the `platform` namespace
  - `"ConfigMap/ops/app-config"` → one specific ConfigMap
  - `"*/platform/*"` → any kind from the `platform` namespace
  - Comma-separated; wildcards (`*`) supported in each segment (`Kind/namespace/name`)

Replicas are marked with:

- `spillway.kroy.io/managed-by=spillway` (annotation + label)
- `spillway.kroy.io/source-from=Kind/namespace/name` (annotation-based replicas)
- `spillway.kroy.io/profile-ref=namespace/name` (profile-managed replicas)

## Behavior notes

- Source labels are copied to replicas (annotation-based mode).
- Spillway-specific annotations are not copied to replicas; non-Spillway annotations are copied.
- Spillway ignores already managed replicas as reconciliation sources (prevents loops).
- `kube-system` is protected by default for `all`/glob name targeting.
- Explicitly naming `kube-system` in `replicate-to` or matching it via `replicate-to-matching` can include it.

## SpillwayProfile CRD

`SpillwayProfile` is a namespaced CRD that declaratively replicates a set of Secrets and ConfigMaps from the profile's own namespace into any number of target namespaces — without annotating the sources.

```yaml
apiVersion: spillway.kroy.io/v1alpha1
kind: SpillwayProfile
metadata:
  name: platform-secrets
  namespace: platform
spec:
  # Target by name/glob, label selector, or both (union).
  targetNamespaces:
    - "team-*"
  targetSelector:
    matchLabels:
      env: prod
  excludeNamespaces:
    - team-dev
  sources:
    - kind: Secret
      name: shared-api-token
      includeKeys: ["token"]          # optional whitelist
    - kind: ConfigMap
      name: app-config
      excludeKeys: ["internal-notes"] # optional blacklist
```

The profile reconciler:
- Creates/updates replicas in all matching namespaces as data changes.
- Removes replicas from namespaces that no longer match (or when the profile is deleted).
- Respects namespace `accept-from` consent annotations.
- Reports `status.replicatedNamespaces`.

Profile replicas carry `spillway.kroy.io/profile-ref` and are independent of annotation-based cleanup — the two mechanisms never interfere with each other.

## Deploy (Helm, recommended)

### Prerequisites

- Kubernetes 1.25+
- Helm 3.10+

### Install from GHCR OCI chart

```bash
# Pick a released chart version from:
# https://github.com/kroy-the-rabbit/spillway/releases
VERSION=0.3.1

helm registry login ghcr.io
helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version "${VERSION}" \
  --namespace spillway-system \
  --create-namespace
```

The chart installs the `SpillwayProfile` CRD by default (`installCRDs: true`). Set `--set installCRDs=false` if you manage CRDs separately.

### Upgrade from GHCR OCI chart

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version "${VERSION}" \
  --namespace spillway-system \
  --reuse-values
```

### Install from local chart path

```bash
VERSION=0.3.1

helm install spillway ./charts/spillway \
  --namespace spillway-system \
  --create-namespace \
  --set image.tag="${VERSION}"
```

### Uninstall

```bash
helm uninstall spillway --namespace spillway-system
```

`helm uninstall` removes the controller, but does not proactively remove previously created replicas. Remove source annotations (or delete SpillwayProfile resources) before uninstall if you want Spillway to clean them up first.

## Production configuration

### High availability

Defaults already run 2 replicas with leader election and a PodDisruptionBudget (`minAvailable: 1`).

Spread replicas across zones:

```yaml
# values-prod.yaml
replicaCount: 2

topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: DoNotSchedule
    labelSelector:
      matchLabels:
        app.kubernetes.io/name: spillway
```

```bash
helm upgrade --install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.3.1 \
  --namespace spillway-system \
  --create-namespace \
  -f values-prod.yaml
```

### Prometheus integration

Enable `ServiceMonitor` (Prometheus Operator required):

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.3.1 \
  --namespace spillway-system \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.labels.release=prometheus
```

### Network policy

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.3.1 \
  --namespace spillway-system \
  --set networkPolicy.enabled=true
```

## Key Helm values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/kroy-the-rabbit/spillway` | Controller image repository |
| `image.tag` | chart `appVersion` | Image tag (`0.3.1` when appVersion is `0.3.1`) |
| `replicaCount` | `2` | Number of controller replicas |
| `installCRDs` | `true` | Install the SpillwayProfile CRD |
| `controller.leaderElect` | `true` | Enable leader election |
| `controller.syncPeriod` | `5m` | Full informer resync interval |
| `controller.selfHealInterval` | `45s` | Per-source fallback requeue (`0` disables) |
| `metrics.service.enabled` | `true` | Expose metrics service |
| `metrics.serviceMonitor.enabled` | `false` | Create ServiceMonitor |
| `podDisruptionBudget.enabled` | `true` | Create PodDisruptionBudget |
| `networkPolicy.enabled` | `true` | Restrict ingress to probe/metrics ports |
| `createNamespace` | `true` | Create release namespace |

See `charts/spillway/values.yaml` for full defaults.

## Deploy (Kustomize, simple/dev)

`config/default` uses image tag `0.3.1` by default. Apply with:

```bash
kubectl apply -k config/default
```

## Build

```bash
# Single-arch
VERSION=0.3.1
docker build --build-arg VERSION="${VERSION}" -t "ghcr.io/kroy-the-rabbit/spillway:${VERSION}" .

# Multi-arch (requires docker buildx)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION="${VERSION}" \
  -t "ghcr.io/kroy-the-rabbit/spillway:${VERSION}" \
  --push .
```

## Run locally (out-of-cluster)

```bash
go run ./cmd/spillway
```

This uses local kubeconfig and requires cluster-wide permissions equivalent to the provided RBAC.

## Examples

| File | Demonstrates |
|------|-------------|
| `examples/secret.yaml` | Basic Secret replication to all namespaces |
| `examples/configmap.yaml` | ConfigMap replication with glob patterns and exclusions |
| `examples/key-projection.yaml` | `include-keys` / `exclude-keys` for partial data sharing |
| `examples/ttl-replicas.yaml` | `replica-ttl` for time-bounded access; permanent expiry semantics |
| `examples/label-selector.yaml` | `replicate-to-matching` with label selectors |
| `examples/namespace-consent.yaml` | Namespace `accept-from` opt-in consent |
| `examples/profile-basic.yaml` | Basic `SpillwayProfile` CRD — no source annotations required |
| `examples/profile-advanced.yaml` | Multi-profile per-source key projection |
| `examples/profile-with-consent.yaml` | `SpillwayProfile` combined with namespace consent |

## Metrics

In addition to controller-runtime metrics, Spillway exports:

- `spillway_replications_total{kind,result}`
- `spillway_reconcile_changes_total{kind,action}`
- `spillway_cleanup_deletes_total{kind}`
- `spillway_replica_remap_failures_total{kind,reason}`

## Limitations

- Spillway uses cluster-wide watches/lists for `Secrets`, `ConfigMaps`, and `Namespaces`; scope and permissions are cluster-level.
- Cleanup is source-driven; if you uninstall without removing source annotations or SpillwayProfile resources first, existing replicas remain.

## Versioning and releases

- Version tags: `vMAJOR.MINOR.PATCH` (example: `v0.3.0`)
- Pre-releases: `vMAJOR.MINOR.PATCH-rc.N`
- Release tags trigger GitHub Actions automation for binaries, container image, Helm OCI chart, and GitHub release assets.
- `charts/spillway/Chart.yaml` `version` and `appVersion` must match the release version (without leading `v`).
