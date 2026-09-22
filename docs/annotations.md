# Annotations

Use these annotations to replicate a `Secret` or `ConfigMap` from the namespace it lives in to other namespaces. Set them on the source object, except `accept-from`, which goes on the target `Namespace`.

## Reference

| Annotation | Set on | Value | Description |
|------------|--------|-------|-------------|
| `spillway.kroy.io/replicate-to` | Secret, ConfigMap | `all`, `*`, globs (`team-*`), names, comma-separated | Target namespaces by name |
| `spillway.kroy.io/replicate-to-matching` | Secret, ConfigMap | Label selector (`env=prod`, `tier=frontend,region=us`) | Target namespaces by label; can be combined with `replicate-to` |
| `spillway.kroy.io/exclude-namespaces` | Secret, ConfigMap | Same syntax as `replicate-to` | Exclusions; always win over include targeting |
| `spillway.kroy.io/force-adopt` | Secret, ConfigMap | `"true"` | Overwrite pre-existing unmanaged target objects and take ownership (requires `--allow-force-adopt`) |
| `spillway.kroy.io/include-keys` | Secret, ConfigMap | `key1,key2` | Whitelist: only listed data keys are copied |
| `spillway.kroy.io/exclude-keys` | Secret, ConfigMap | `key1,key2` | Blacklist: listed data keys are omitted |
| `spillway.kroy.io/replica-ttl` | Secret, ConfigMap | Go duration (`24h`, `168h`, `30m`) | Replicas expire and are permanently deleted after this duration |
| `spillway.kroy.io/accept-from` | Namespace | `Kind/namespace/name` patterns, comma-separated | Consent filter: which sources may replicate into this namespace |

Spillway writes these markers itself; do not set them by hand:

| Marker | Where | Meaning |
|--------|-------|---------|
| `spillway.kroy.io/managed-by=spillway` | Replica (annotation and label) | Object is a Spillway replica |
| `spillway.kroy.io/source-from=Kind/namespace/name` | Replica (annotation) | Source of an annotation-based replica |
| `spillway.kroy.io/profile-ref=namespace/name` | Replica (annotation) | Profile that manages a profile-based replica |
| `spillway.kroy.io/expires-at` | Replica (annotation) | RFC 3339 expiry time, stamped when `replica-ttl` is set |
| `spillway.kroy.io/expired-namespaces` | Source (annotation) | Namespaces whose TTL replica has expired and will not be recreated |

## Target selection

- `spillway.kroy.io/replicate-to`
    - Comma-separated namespace targets
    - Supports `all` or `*`, globs like `team-*`, explicit names like `payments`, or mixed forms
- `spillway.kroy.io/replicate-to-matching`
    - Kubernetes label selector for namespace labels (for example `env=prod` or `tier=frontend,region=us`)
    - Can be used together with `replicate-to`
- `spillway.kroy.io/exclude-namespaces`
    - Optional comma-separated exclusions, same syntax as `replicate-to`
    - Always takes precedence over include targeting

Spillway watches namespace create events and label changes, so a new namespace that matches a glob or selector receives replicas promptly, and a namespace that stops matching has its replica removed.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-settings
  namespace: source-team
  annotations:
    spillway.kroy.io/replicate-to: "team-*,sandbox"
    spillway.kroy.io/exclude-namespaces: "team-dev"
data:
  LOG_LEVEL: info
  FEATURE_FLAG_X: "true"
```

Label selectors follow Kubernetes conventions: `key=value`, `key!=value`, `key in (a,b)`, `key notin (a,b)`, and `key` (existence). Comma-separated terms are ANDed.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: monitoring-token
  namespace: observability
  annotations:
    spillway.kroy.io/replicate-to: "infra-*"
    spillway.kroy.io/replicate-to-matching: "monitoring=enabled"
type: Opaque
stringData:
  token: replace-me
```

## Ownership and adoption

- `spillway.kroy.io/force-adopt: "true"`
    - Allows overwriting pre-existing unmanaged target objects and taking ownership
    - Default behavior is to skip conflicting unmanaged objects

The controller ignores this annotation unless it runs with `--allow-force-adopt` (Helm: `controller.allowForceAdopt=true`). Skipped conflicts are reported with a `ReplicationSkipped` event on the source.

## Key projection

- `spillway.kroy.io/include-keys: "key1,key2"`
    - Whitelist: only the listed data keys are copied into replicas
    - Mutually exclusive with `exclude-keys`; `include-keys` takes precedence
- `spillway.kroy.io/exclude-keys: "key1,key2"`
    - Blacklist: the listed data keys are omitted from replicas

For typed Secrets, Spillway preserves the source `type` only when the projected payload still satisfies Kubernetes validation. For example, projecting only `tls.crt` from a `kubernetes.io/tls` Secret produces an `Opaque` replica; the replica stays `kubernetes.io/tls` only when both `tls.crt` and `tls.key` are present after projection.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: service-credentials
  namespace: platform
  annotations:
    spillway.kroy.io/replicate-to: "all"
    spillway.kroy.io/include-keys: "readonly-token"
type: Opaque
stringData:
  readonly-token: read-only-value
  admin-token: super-secret         # NOT replicated
  internal-key: another-secret      # NOT replicated
```

## Replica TTL

- `spillway.kroy.io/replica-ttl: "24h"`
    - Go duration (e.g. `24h`, `168h`, `30m`). Replicas are stamped with an `expires-at` annotation at creation time.
    - When the TTL elapses the replica is **permanently deleted**; it is **not recreated**. The expired namespace is recorded in `spillway.kroy.io/expired-namespaces` on the source object and skipped on all future reconciles.
    - To re-enable replication to an expired namespace: manually remove that namespace from the `expired-namespaces` annotation value, or delete the annotation entirely.
    - Removing `replica-ttl` from a source that has `expired-namespaces` recorded automatically clears the expired record and resumes replication on the next reconcile.

!!! warning "Expiry is permanent"
    An expired replica is deleted and not recreated, even if the source changes. Edit or remove `spillway.kroy.io/expired-namespaces` on the source to replicate to that namespace again.

Go durations have no day unit; write `168h` for a week.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: temp-access-token
  namespace: platform
  annotations:
    spillway.kroy.io/replicate-to: "staging"
    spillway.kroy.io/replica-ttl: "8h"
type: Opaque
stringData:
  token: short-lived-token-value
```

## Namespace consent

- `spillway.kroy.io/accept-from` on the **target Namespace** (not the source object)
    - Absent: accept from all sources (backward-compatible default)
    - `"all"` or `"*"`: accept from all sources
    - `"Secret/platform/*"`: any Secret from the `platform` namespace
    - `"ConfigMap/ops/app-config"`: one specific ConfigMap
    - `"*/platform/*"`: any kind from the `platform` namespace
    - Comma-separated; wildcards (`*`) supported in each segment (`Kind/namespace/name`)

Profile sources appear in `accept-from` as `Kind/profileNamespace/sourceName`, the same form as annotation-based sources.

!!! warning "Deny by default"
    With the default configuration a namespace without `accept-from` accepts everything. Run the controller with `--require-namespace-consent` (Helm: `controller.requireNamespaceConsent=true`) to flip this: namespaces then receive replicas only if they carry an `accept-from` annotation that matches the source.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: tenant-a
  annotations:
    spillway.kroy.io/accept-from: "Secret/platform/*,ConfigMap/ops/app-config"
```

## Behavior notes

- Source labels are copied to replicas (annotation-based mode).
- Spillway-specific annotations are not copied to replicas; non-Spillway annotations are copied, except keys with a denied prefix (see [Operations](operations.md#annotation-filtering)).
- Spillway ignores already managed replicas as reconciliation sources (prevents loops).
- `kube-system` is protected by default for `all`/glob name targeting.
- Explicitly naming `kube-system` in `replicate-to` or matching it via `replicate-to-matching` can include it.
- Removing replication annotations triggers cleanup of now-out-of-scope replicas. Deleting a source object triggers finalizer-based replica cleanup.
- Deleted replicas are recreated on the next reconcile while the source still targets the namespace.

## Examples

| File | Demonstrates |
|------|-------------|
| [`examples/secret.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/secret.yaml) | Basic Secret replication to all namespaces |
| [`examples/configmap.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/configmap.yaml) | ConfigMap replication with glob patterns and exclusions |
| [`examples/key-projection.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/key-projection.yaml) | `include-keys` / `exclude-keys` for partial data sharing |
| [`examples/ttl-replicas.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/ttl-replicas.yaml) | `replica-ttl` for time-bounded access; permanent expiry semantics |
| [`examples/label-selector.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/label-selector.yaml) | `replicate-to-matching` with label selectors |
| [`examples/namespace-consent.yaml`](https://github.com/kroy-the-rabbit/spillway/blob/main/examples/namespace-consent.yaml) | Namespace `accept-from` opt-in consent |
