# spillway

Replicates Kubernetes Secrets and ConfigMaps across namespaces in near real time using source annotations or `SpillwayProfile` resources.

## Overview

spillway is a lightweight Kubernetes controller that watches annotated Secrets and ConfigMaps and copies them to target namespaces. It is designed for platform teams that need to distribute TLS certificates, registry pull secrets, database credentials, or shared configuration to many namespaces without writing custom controllers or managing duplicate resources by hand.

## Features

- **Annotation-driven replication** — add `spillway.kroy.io/replicate-to` to any Secret or ConfigMap and spillway replicates it immediately
- **Label-selector targeting** — use `spillway.kroy.io/replicate-to-matching` to target namespaces by label rather than by name
- **SpillwayProfile CRD** — declaratively manage which secrets and configmaps are distributed to which namespaces, without touching source objects
- **Key projection** — copy only specific keys from a source via `spillway.kroy.io/include-keys` / `spillway.kroy.io/exclude-keys`
- **TTL replicas** — set `spillway.kroy.io/replica-ttl` to automatically expire replicas after a duration
- **Deny-by-default consent** — namespaces opt in to receive replicas via `spillway.kroy.io/accept-from`
- **Protected namespaces** — `kube-system` (and any configured list) is shielded from wildcard replication
- **Structured audit log** — every create, update, delete, conflict, and expiry is logged with source/target/action/reason fields
- **HA-ready** — leader election, PodDisruptionBudget, and soft pod anti-affinity by default

## Installation

```bash
helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --namespace spillway-system \
  --create-namespace
```

Or with a values file:

```bash
helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --namespace spillway-system \
  --create-namespace \
  -f my-values.yaml
```

## Quick start

### Annotation-driven

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: registry-pull-secret
  namespace: platform
  annotations:
    spillway.kroy.io/replicate-to: "team-a, team-b, team-c"
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: <base64>
```

spillway creates `registry-pull-secret` in `team-a`, `team-b`, and `team-c` and keeps it in sync whenever the source changes.

### Label-selector targeting

```yaml
annotations:
  spillway.kroy.io/replicate-to-matching: "env=production"
```

Replicates to every namespace with the label `env=production` — including namespaces created after the source.

### SpillwayProfile

```yaml
apiVersion: spillway.kroy.io/v1alpha1
kind: SpillwayProfile
metadata:
  name: platform-secrets
  namespace: platform
spec:
  targetNamespaces: ["team-a", "team-b"]
  sources:
    - kind: Secret
      name: registry-pull-secret
    - kind: ConfigMap
      name: cluster-ca-bundle
```

## Security model

### Namespace consent

By default, any namespace can receive replicas (allow-by-default). To require namespaces to opt in:

```bash
helm upgrade spillway ... --set controller.requireNamespaceConsent=true
```

Namespaces then need:

```yaml
annotations:
  spillway.kroy.io/accept-from: "Secret/platform/*, ConfigMap/platform/*"
```

### Protected namespaces

`kube-system` is protected from wildcard and glob replication by default. Extend the list:

```yaml
controller:
  protectedNamespaces: "kube-system,kube-public,cert-manager"
```

### Force-adopt

Disabled by default. Enable only if you need spillway to take ownership of pre-existing unmanaged objects:

```yaml
controller:
  allowForceAdopt: true
```

### Annotation filtering

Spillway never copies `kubectl.kubernetes.io/`, `helm.sh/`, `argocd.argoproj.io/`, and other controller-specific annotation prefixes to replicas. Override the list:

```yaml
controller:
  annotationDenyPrefixes: "kubectl.kubernetes.io/,meta.helm.sh/,my-injector.io/"
```

## Values reference

| Key | Default | Description |
|-----|---------|-------------|
| `replicaCount` | `2` | Number of controller replicas |
| `image.repository` | `ghcr.io/kroy-the-rabbit/spillway` | Image repository |
| `image.tag` | `""` | Image tag (defaults to chart appVersion) |
| `controller.leaderElect` | `true` | Enable leader election |
| `controller.syncPeriod` | `5m` | Full resync interval |
| `controller.selfHealInterval` | `45s` | Per-object self-heal requeue interval |
| `controller.orphanAuditInterval` | `0s` | Orphan audit interval (0 = disabled) |
| `controller.requireNamespaceConsent` | `false` | Deny-by-default namespace consent |
| `controller.allowForceAdopt` | `false` | Allow force-adopt annotation |
| `controller.protectedNamespaces` | `""` | Comma-separated protected namespace names |
| `controller.annotationDenyPrefixes` | `""` | Comma-separated annotation prefix denylist |
| `resources.requests.cpu` | `50m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `256Mi` | Memory limit |
| `metrics.service.enabled` | `true` | Expose a metrics ClusterIP Service |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus Operator ServiceMonitor |
| `podDisruptionBudget.enabled` | `true` | Create a PodDisruptionBudget |
| `networkPolicy.enabled` | `true` | Create a NetworkPolicy |
| `networkPolicy.egressEnabled` | `false` | Add egress rules to the NetworkPolicy |
| `installCRDs` | `true` | Install the SpillwayProfile CRD |
| `createNamespace` | `true` | Create the release namespace |

For the full values reference see [values.yaml](values.yaml).

## Annotations reference

| Annotation | Object | Description |
|------------|--------|-------------|
| `spillway.kroy.io/replicate-to` | Secret, ConfigMap | Comma-separated target namespace names or globs |
| `spillway.kroy.io/replicate-to-matching` | Secret, ConfigMap | Label selector targeting namespaces |
| `spillway.kroy.io/exclude-namespaces` | Secret, ConfigMap | Namespaces to exclude from replication |
| `spillway.kroy.io/include-keys` | Secret, ConfigMap | Comma-separated keys to include in replicas |
| `spillway.kroy.io/exclude-keys` | Secret, ConfigMap | Comma-separated keys to exclude from replicas |
| `spillway.kroy.io/replica-ttl` | Secret, ConfigMap | Go duration after which replicas expire |
| `spillway.kroy.io/force-adopt` | Secret, ConfigMap | Take ownership of pre-existing unmanaged objects (requires `--allow-force-adopt`) |
| `spillway.kroy.io/accept-from` | Namespace | Consent filter: `Kind/namespace/name` patterns |

## Links

- [Website](https://spillway.kroy.io)
- [Source](https://github.com/kroy-the-rabbit/spillway)
- [Examples](https://github.com/kroy-the-rabbit/spillway/tree/main/examples)
