# Install

This page shows how to install, upgrade, and remove Spillway with Helm, how to deploy with Kustomize for development, and which values to set in production.

## Prerequisites

- Kubernetes 1.35 through 1.37
- Helm 3.10+

Spillway is tested against the last three Kubernetes minor releases, currently 1.35 through 1.37 (it is built on `k8s.io/client-go` v0.37). Older versions may work but are unsupported.

## Helm

Pick a released chart version from <https://github.com/kroy-the-rabbit/spillway/releases>. The current release is `0.6.0`.

=== "GHCR OCI chart"

    ```bash
    VERSION=0.6.0

    helm registry login ghcr.io
    helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
      --version "${VERSION}" \
      --namespace spillway-system \
      --create-namespace
    ```

=== "Local chart path"

    ```bash
    VERSION=0.6.0

    helm install spillway ./charts/spillway \
      --namespace spillway-system \
      --create-namespace \
      --set image.tag="${VERSION}"
    ```

The chart installs the `SpillwayProfile` CRD by default (`installCRDs: true`). Set `--set installCRDs=false` if you manage CRDs separately.

### Upgrade

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version "${VERSION}" \
  --namespace spillway-system \
  --reuse-values
```

For the move from 0.x to 1.0, read [Upgrading](upgrading.md) first.

### Uninstall

```bash
helm uninstall spillway --namespace spillway-system
```

`helm uninstall` removes the controller, but does not remove previously created replicas. Remove source annotations (or delete `SpillwayProfile` resources) before uninstall if you want Spillway to clean them up first.

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
  --version 0.6.0 \
  --namespace spillway-system \
  --create-namespace \
  -f values-prod.yaml
```

### Prometheus integration

Enable `ServiceMonitor` (Prometheus Operator required):

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.6.0 \
  --namespace spillway-system \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.labels.release=prometheus
```

### Network policy

The chart creates a NetworkPolicy by default. It leaves probes reachable and limits metrics ingress to pods in the release namespace. To set it explicitly:

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --version 0.6.0 \
  --namespace spillway-system \
  --set networkPolicy.enabled=true
```

Add `networkPolicy.ingress` rules if your scraper runs in another namespace.

### Namespace consent and force-adopt

Both are off by default. Enable them through the `controller` values; see [Security](security.md) for what they do.

```yaml
controller:
  requireNamespaceConsent: true
  allowForceAdopt: false
  protectedNamespaces: "kube-system,kube-public,cert-manager"
```

## Key Helm values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/kroy-the-rabbit/spillway` | Controller image repository |
| `image.tag` | chart `appVersion` | Image tag (`0.6.0` when appVersion is `0.6.0`) |
| `replicaCount` | `2` | Number of controller replicas |
| `installCRDs` | `true` | Install the SpillwayProfile CRD |
| `controller.leaderElect` | `true` | Enable leader election |
| `controller.syncPeriod` | `5m` | Full informer resync interval |
| `controller.selfHealInterval` | `45s` | Per-source fallback requeue (`0` disables) |
| `controller.orphanAuditInterval` | `0s` | Periodic orphaned annotation-replica cleanup (`0` disables) |
| `controller.requireNamespaceConsent` | `false` | Deny-by-default namespace consent |
| `controller.allowForceAdopt` | `false` | Allow the `force-adopt` annotation |
| `controller.protectedNamespaces` | `""` | Comma-separated protected namespace names (`kube-system` when empty) |
| `controller.annotationDenyPrefixes` | `""` | Comma-separated annotation prefixes never copied to replicas (overrides the built-in list) |
| `resources.requests.cpu` | `50m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `256Mi` | Memory limit |
| `metrics.service.enabled` | `true` | Expose metrics service |
| `metrics.serviceMonitor.enabled` | `false` | Create ServiceMonitor |
| `podDisruptionBudget.enabled` | `true` | Create PodDisruptionBudget |
| `networkPolicy.enabled` | `true` | Leave probes reachable and limit metrics ingress to same-namespace pods by default |
| `networkPolicy.egressEnabled` | `false` | Add egress rules to the NetworkPolicy |
| `createNamespace` | `true` | Create release namespace |

See [`charts/spillway/values.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/charts/spillway/values.yaml) for full defaults.

## Kustomize (simple/dev)

`config/default` uses image tag `0.6.0` by default. Apply with:

```bash
kubectl apply -k config/default
```

The Kustomize manifests track the chart but are a convenience for development; they are not covered by the [compatibility promise](upgrading.md#compatibility-promise-1x).

## Build the image

```bash
# Single-arch
VERSION=0.6.0
docker build --build-arg VERSION="${VERSION}" -t "ghcr.io/kroy-the-rabbit/spillway:${VERSION}" .

# Multi-arch (requires docker buildx)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION="${VERSION}" \
  -t "ghcr.io/kroy-the-rabbit/spillway:${VERSION}" \
  --push .
```
