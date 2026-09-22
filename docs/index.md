# Spillway

Spillway replicates Kubernetes `Secrets` and `ConfigMaps` across namespaces in near real time. You choose how to drive it:

- **Annotation-driven**: add one annotation to an existing source object.
- **SpillwayProfile**: a namespaced CRD that lists sources and targets, with no annotations on the sources.

Replicas are same-name copies in each target namespace. Spillway keeps them in sync with the source, recreates them if they are deleted, and removes them when the source stops targeting the namespace or is deleted.

## Install in 30 seconds

```bash
helm install spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --namespace spillway-system \
  --create-namespace
```

Then annotate a source:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: shared-api-token
  namespace: platform
  annotations:
    spillway.kroy.io/replicate-to: "all"
type: Opaque
stringData:
  token: replace-me
```

Spillway creates `shared-api-token` in every non-system namespace and keeps it updated. See [Install](install.md) for pinned versions, upgrades, and production values.

## Where to go next

| Page | Read it when you want to |
|------|--------------------------|
| [Install](install.md) | Install or upgrade with Helm or Kustomize, and tune HA, metrics, and network policy. |
| [Annotations](annotations.md) | Target namespaces, project keys, set TTLs, and require namespace consent. |
| [SpillwayProfile](profiles.md) | Replicate without annotating sources, using the CRD. |
| [Operations](operations.md) | Look up controller flags, metrics, events, and limitations. |
| [Security](security.md) | Review the RBAC scope, restrict who can replicate, and verify release signatures. |
| [Upgrading](upgrading.md) | Move to 1.0 and read the 1.x compatibility promise. |
| [Changelog](changelog.md) | See what changed in each release. |

## Project links

- Source: <https://github.com/kroy-the-rabbit/spillway>
- Helm chart (OCI): `oci://ghcr.io/kroy-the-rabbit/charts/spillway`
- Container image: `ghcr.io/kroy-the-rabbit/spillway`
- Examples: <https://github.com/kroy-the-rabbit/spillway/tree/main/examples>
