# SpillwayProfile

Use a `SpillwayProfile` to replicate a set of Secrets and ConfigMaps without annotating the sources. The profile is a namespaced CRD; it lives in the namespace that owns the sources and lists what to replicate and where.

## API

- Group/version: `spillway.kroy.io/v1` (storage version). `v1alpha1` is still served, marked deprecated, and is removed no earlier than 1.2. The schemas are identical.
- Kind: `SpillwayProfile`, short name `swp`.
- Scope: namespaced. Sources must be in the profile's own namespace.

Install the CRD with the Helm chart (`installCRDs: true`, the default) or apply `config/crd` yourself.

## Example

```yaml
apiVersion: spillway.kroy.io/v1
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

## Spec

| Field | Type | Description |
|-------|------|-------------|
| `targetNamespaces` | `[]string` | Namespace names or globs, same syntax as `replicate-to`. Union with `targetSelector`. |
| `targetSelector` | `LabelSelector` | Select target namespaces by label. `matchExpressions` operators must be `In`, `NotIn`, `Exists`, or `DoesNotExist`. |
| `excludeNamespaces` | `[]string` | Names or globs never targeted. Exclusions always win. |
| `sources` | `[]ProfileSource` | Required, at least one entry. Each source is replicated independently. |
| `sources[].kind` | `Secret` or `ConfigMap` | Kind of the source object. |
| `sources[].name` | `string` | Name of the source in the profile's namespace. Required. |
| `sources[].includeKeys` | `[]string` | Whitelist of data keys. Mutually exclusive with `excludeKeys`. |
| `sources[].excludeKeys` | `[]string` | Blacklist of data keys. |

Validation is enforced by the CRD schema and CEL rules, so an invalid profile is rejected at admission. Key projection follows the same rules as the annotations, including the `Opaque` coercion for partially projected typed Secrets (see [Key projection](annotations.md#key-projection)).

## Reconciler behavior

The profile reconciler:

- Creates/updates replicas in all matching namespaces as data changes.
- Removes replicas from namespaces that no longer match (or when the profile is deleted).
- Enforces namespace `accept-from` consent annotations per source object. Profile sources appear in `accept-from` as `Kind/profileNamespace/sourceName`.
- Refuses to overwrite pre-existing unmanaged objects or annotation-managed replicas.
- Reports `status.replicatedNamespaces`.

Profile replicas carry `spillway.kroy.io/profile-ref` and are independent of annotation-based cleanup. The two mechanisms never interfere with each other.

Protected namespaces (`kube-system` by default) are skipped for globs and selectors, exactly as for annotations.

## Status

| Field | Description |
|-------|-------------|
| `status.replicatedNamespaces` | Namespaces currently receiving replicas from this profile. |
| `status.conditions[type=SourcesAvailable]` | `True` (`AllSourcesFound`) when every listed source exists in the profile namespace; `False` (`SourceMissing`) otherwise. |
| `status.conditions[type=Ready]` | `True` (`SyncSucceeded`) when the last reconcile wrote every replica without error; `False` (`SyncFailed`) otherwise. |

```bash
kubectl get swp -n platform
kubectl describe swp platform-secrets -n platform
```

Reconcile outcomes are also recorded as Events on the profile (`ReplicationSucceeded`, `ReplicationFailed`, `InvalidSelector`) and counted in `spillway_replication_outcomes_total{mode="profile"}`.

## Consent with profiles

Combine a wide profile with `accept-from` on tenant namespaces for a pull-based model: the platform team defines what can be replicated, and each tenant opts in.

```yaml
apiVersion: spillway.kroy.io/v1
kind: SpillwayProfile
metadata:
  name: platform-bundle
  namespace: platform
spec:
  targetNamespaces:
    - "*"
  excludeNamespaces:
    - kube-system
    - kube-public
    - spillway-system
  sources:
    - kind: Secret
      name: registry-pull-secret
    - kind: ConfigMap
      name: tls-bundle
---
apiVersion: v1
kind: Namespace
metadata:
  name: team-payments
  annotations:
    spillway.kroy.io/accept-from: "Secret/platform/registry-pull-secret"
```

## Examples

| File | Demonstrates |
|------|-------------|
| [`examples/profile-basic.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/profile-basic.yaml) | Basic `SpillwayProfile`; no source annotations required |
| [`examples/profile-advanced.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/profile-advanced.yaml) | Multi-profile per-source key projection |
| [`examples/profile-with-consent.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/profile-with-consent.yaml) | `SpillwayProfile` combined with namespace consent |
