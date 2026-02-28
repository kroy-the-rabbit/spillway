# spillway

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/spillway)](https://artifacthub.io/packages/search?repo=spillway)

`spillway` replicates Kubernetes `Secrets` and `ConfigMaps` across namespaces in near real time using source-object annotations.

## Project links

- Website (GitHub Pages): https://spillway.kroy.io
- Source: https://github.com/kroy-the-rabbit/spillway
- Helm chart (OCI): `oci://ghcr.io/kroy-the-rabbit/charts/spillway`
- Container image: `ghcr.io/kroy-the-rabbit/spillway`

The website is published from the `docs/` directory by `.github/workflows/deploy-pages.yaml` on pushes to `main`.

## How it works

- Add replication annotations to a source `Secret` or `ConfigMap`.
- Spillway reconciles same-name replicas in target namespaces.
- Removing replication annotations triggers cleanup of now-out-of-scope replicas.
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

Replicas are marked with:

- `spillway.kroy.io/managed-by=spillway` (annotation + label)
- `spillway.kroy.io/source-from=Kind/namespace/name`

## Behavior notes

- Source labels are copied to replicas.
- Spillway-specific annotations are not copied to replicas; non-Spillway annotations are copied.
- Spillway ignores already managed replicas as reconciliation sources (prevents loops).
- `kube-system` is protected by default for `all`/glob name targeting.
- Explicitly naming `kube-system` in `replicate-to` or matching it via `replicate-to-matching` can include it.

## Deploy (Helm, recommended)

### Prerequisites

- Kubernetes 1.25+
- Helm 3.10+

### Install from GHCR OCI chart

```bash
# Pick a released chart version from:
# https://github.com/kroy-the-rabbit/spillway/releases
VERSION=0.2.5

helm registry login ghcr.io
helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version "${VERSION}" \
  --namespace spillway-system \
  --create-namespace
```

### Upgrade from GHCR OCI chart

```bash
# Reuse the same VERSION value used at install time.
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version "${VERSION}" \
  --namespace spillway-system
```

### Install from local chart path

```bash
# Pick a released app image tag from:
# https://github.com/kroy-the-rabbit/spillway/releases
VERSION=0.2.5

helm install spillway ./charts/spillway \
  --namespace spillway-system \
  --create-namespace \
  --set image.tag="${VERSION}"
```

### Uninstall

```bash
helm uninstall spillway --namespace spillway-system
```

`helm uninstall` removes the controller, but it does not proactively remove previously created replicas. Remove source annotations before uninstall if you want Spillway to clean them up first.

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
  --version 0.2.5 \
  --namespace spillway-system \
  --create-namespace \
  -f values-prod.yaml
```

### Prometheus integration

Enable `ServiceMonitor` (Prometheus Operator required):

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.2.5 \
  --namespace spillway-system \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.labels.release=prometheus
```

### Network policy

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.2.5 \
  --namespace spillway-system \
  --set networkPolicy.enabled=true
```

## Key Helm values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/kroy-the-rabbit/spillway` | Controller image repository |
| `image.tag` | chart `appVersion` | Image tag (`0.2.5` when appVersion is `0.2.5`) |
| `replicaCount` | `2` | Number of controller replicas |
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

`config/default` uses image tag `0.2.5` by default. Apply with:

```bash
kubectl apply -k config/default
```

## Build

```bash
# Single-arch
VERSION=0.2.5
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

- `examples/secret.yaml`
- `examples/configmap.yaml`

## Metrics

In addition to controller-runtime metrics, Spillway exports:

- `spillway_replications_total{kind,result}`
- `spillway_reconcile_changes_total{kind,action}`
- `spillway_cleanup_deletes_total{kind}`
- `spillway_replica_remap_failures_total{kind,reason}`

## Limitations

- Spillway uses cluster-wide watches/lists for `Secrets`, `ConfigMaps`, and `Namespaces`; scope and permissions are cluster-level.
- Cleanup is source-driven; if you uninstall without removing source annotations first, existing replicas remain.

## Versioning and releases

- Version tags: `vMAJOR.MINOR.PATCH` (example: `v0.2.5`)
- Pre-releases: `vMAJOR.MINOR.PATCH-rc.N`
- Release tags trigger GitHub Actions automation for binaries, container image, Helm OCI chart, and GitHub release assets.
- `charts/spillway/Chart.yaml` `version` and `appVersion` must match the release version (without leading `v`).
