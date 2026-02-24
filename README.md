# spillway

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/spillway)](https://artifacthub.io/packages/search?repo=spillway)

`spillway` replicates Kubernetes `Secrets` and `ConfigMaps` to other namespaces in near real time using annotations on the source object.

## How it works

- Add `spillway.kroy.io/replicate-to` to a source `Secret` or `ConfigMap`.
- The controller watches all namespaces.
- It creates or updates same-name replicas in target namespaces.
- If the annotation is removed, targets are deleted.
- If the source is deleted, replicas are deleted via a finalizer.

## Annotation contract

- `spillway.kroy.io/replicate-to`: Comma-separated targets with support for:
  - `all` (or `*`) for every namespace except the source namespace
  - wildcard patterns like `team-*`
  - specific namespaces like `payments`
  - mixed values like `team-*,sandbox,prod`
- `spillway.kroy.io/exclude-namespaces`: Optional comma-separated exclusions using the same syntax
  - Example: `team-dev,team-qa-*`

Replicas are marked with metadata so Spillway can identify and reconcile them:

- `spillway.kroy.io/managed-by=spillway`
- `spillway.kroy.io/source-from` (format: `Kind/namespace/name`)

## Behavior notes

- Replicas use the same object name as the source.
- Source labels are copied to replicas.
- Non-Spillway annotations are copied to replicas.
- Spillway ignores objects that are already managed replicas (prevents replication loops).
- `all`/wildcard resolution is evaluated against current namespaces during reconcile.
- Newly created namespaces are picked up on the next source reconcile or periodic resync.
- `kube-system` is excluded by default when matched through `all` or wildcard selectors.
- Explicitly naming `kube-system` in `spillway.kroy.io/replicate-to` overrides that default.
- `spillway.kroy.io/exclude-namespaces` always takes precedence over includes.

---

## Deploy (Helm — recommended)

### Prerequisites

- Kubernetes 1.25+
- Helm 3.10+

### Install (from GitHub Container Registry OCI chart)

```bash
helm registry login ghcr.io
helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.1.0 \
  --namespace spillway-system \
  --create-namespace
```

### Install (from local repo path)

```bash
helm install spillway ./charts/spillway \
  --namespace spillway-system \
  --create-namespace \
  --set image.tag=v0.1.0
```

### Upgrade

```bash
helm upgrade spillway ./charts/spillway \
  --namespace spillway-system \
  --set image.tag=v0.2.0
```

### Uninstall

```bash
helm uninstall spillway --namespace spillway-system
```

> **Note**: Helm uninstall removes the controller but does not delete existing replicas
> that Spillway created in other namespaces. Those are cleaned up automatically when
> the source object's annotation is removed before uninstalling.

---

## Production configuration

### High availability

Run 2 replicas with leader election (only one actively reconciles; the other stands by):

```bash
helm install spillway ./charts/spillway \
  --namespace spillway-system \
  --set image.tag=v0.1.0 \
  --set replicaCount=2 \
  --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.minAvailable=1
```

Spread replicas across availability zones:

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

podDisruptionBudget:
  enabled: true
  minAvailable: 1
```

```bash
helm install spillway ./charts/spillway \
  --namespace spillway-system \
  -f values-prod.yaml \
  --set image.tag=v0.1.0
```

### Prometheus metrics

Enable a `ServiceMonitor` (requires [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator)):

```bash
helm upgrade spillway ./charts/spillway \
  --namespace spillway-system \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.labels.release=prometheus
```

### Network policy

Restrict ingress to only the metrics and probe ports:

```bash
helm upgrade spillway ./charts/spillway \
  --namespace spillway-system \
  --set networkPolicy.enabled=true
```

### Private registry

```yaml
imagePullSecrets:
  - name: my-registry-credentials

serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/spillway
```

---

## Key values reference

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/kroy-the-rabbit/spillway` | Image repository |
| `image.tag` | chart `appVersion` | Image tag |
| `replicaCount` | `1` | Number of controller replicas |
| `controller.leaderElect` | `true` | Enable leader election |
| `controller.syncPeriod` | `5m` | Full resync interval |
| `resources` | see values.yaml | CPU/memory requests and limits |
| `podDisruptionBudget.enabled` | `false` | Create a PodDisruptionBudget |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus ServiceMonitor |
| `networkPolicy.enabled` | `false` | Restrict ingress via NetworkPolicy |
| `createNamespace` | `true` | Create the release namespace |

Full defaults are documented in [`charts/spillway/values.yaml`](charts/spillway/values.yaml).

---

## Deploy (Kustomize — simple/dev)

Build and push an image, then update `config/manager/deployment.yaml` with your image.

```bash
kubectl apply -k config/default
```

---

## Build

```bash
# Single-arch
docker build --build-arg VERSION=v0.1.0 -t ghcr.io/kroy-the-rabbit/spillway:v0.1.0 .

# Multi-arch (requires docker buildx)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=v0.1.0 \
  -t ghcr.io/kroy-the-rabbit/spillway:v0.1.0 \
  --push .
```

## Run locally (out-of-cluster)

```bash
go run ./cmd/spillway
```

This uses your local kubeconfig and requires cluster-wide permissions equivalent to the provided RBAC.

---

## Examples

- `examples/configmap.yaml`
- `examples/secret.yaml`

---

## Limitations

- Namespace targeting is annotation-based (`all`, globs, and explicit names), but namespace creation events are not watched (new namespaces are picked up on the next reconcile or periodic resync).
- Cluster-wide cleanup lists all `Secrets`/`ConfigMaps` and filters in memory.
- No custom metrics beyond controller-runtime defaults.

## Versioning And Releases

- Spillway uses semantic versioning tags: `vMAJOR.MINOR.PATCH` (for example `v0.1.0`).
- Until `v1.0.0`, minor versions may include annotation or behavior changes.
- Patch releases are for backward-compatible fixes only.
- Pre-releases use semver prerelease tags (for example `v0.2.0-rc.1`).
- Git tags trigger GitHub Actions release automation (image build, Helm chart publish, GitHub release artifacts).
- Helm chart `version` and `appVersion` should match the controller release version (without the leading `v`) for normal releases.

## Best Practice (Default)

- Excluding `kube-system` by default is a good baseline to reduce blast radius from accidental `all` replication.
- Keep replication into system namespaces opt-in by explicit include.
- Use `spillway.kroy.io/exclude-namespaces` for team-specific guardrails (`dev`, `scratch`, etc.).
