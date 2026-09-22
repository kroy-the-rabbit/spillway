# Operations

This page lists the controller's flags, the metrics and events it emits, how to run it outside a cluster, and what it does not do.

## Controller flags

The Helm chart sets these from the `controller` values. Print the live list with `spillway --help`.

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics-bind-address` | `:8080` | Address the metrics endpoint binds to |
| `--health-probe-bind-address` | `:8081` | Address the probe endpoint binds to (`/healthz`, `/readyz`) |
| `--leader-elect` | `true` | Enable leader election (lease `spillway.spillway.kroy.io`) |
| `--sync-period` | `5m` | Periodic informer resync interval |
| `--self-heal-interval` | `45s` | Per-object self-heal fallback requeue interval (`0` disables) |
| `--orphan-audit-interval` | `0` | Periodic audit that deletes orphaned annotation-managed replicas (`0` disables) |
| `--protected-namespaces` | `""` (`kube-system`) | Comma-separated namespaces protected from wildcard and selector replication |
| `--require-namespace-consent` | `false` | Deny by default: namespaces must opt in via `accept-from` to receive replicas |
| `--allow-force-adopt` | `false` | Honor the `force-adopt` annotation on sources |
| `--annotation-deny-prefixes` | `""` (built-in list) | Comma-separated annotation key prefixes never copied to replicas; replaces the built-in list when set |
| `--version` | `false` | Print the version and exit |

Logging flags come from controller-runtime's zap integration: `--zap-log-level`, `--zap-encoder`, `--zap-devel`, `--zap-stacktrace-level`, `--zap-time-encoding`. `--kubeconfig` selects a kubeconfig file when running out of cluster.

### Annotation filtering

Non-Spillway annotations on a source are copied to its replicas, except keys that start with a denied prefix. The built-in list is:

```text
kubectl.kubernetes.io/
meta.helm.sh/
helm.sh/
argocd.argoproj.io/
config.kubernetes.io/
kustomize.toolkit.fluxcd.io/
fluxcd.io/
weave.works/
```

Setting `--annotation-deny-prefixes` (Helm: `controller.annotationDenyPrefixes`) replaces the list; include the defaults you still want.

## Metrics

Metrics are served on `--metrics-bind-address` in Prometheus format. In addition to controller-runtime metrics, Spillway exports:

| Metric | Labels | Description |
|--------|--------|-------------|
| `spillway_replications_total` | `kind`, `result` | Replication attempts per source kind and result |
| `spillway_replication_outcomes_total` | `kind`, `mode`, `outcome` | Outcomes per kind and mode (`annotation` or `profile`) |
| `spillway_reconcile_changes_total` | `kind`, `action` | Replica creates, updates, and deletes performed by reconciles |
| `spillway_cleanup_deletes_total` | `kind` | Replicas deleted by cleanup (source deleted or out of scope) |
| `spillway_replica_remap_failures_total` | `kind`, `reason` | Replica events that could not be mapped back to a source |

Enable a `ServiceMonitor` with `metrics.serviceMonitor.enabled=true`; see [Install](install.md#prometheus-integration).

## Events

Spillway records Kubernetes Events (`events.k8s.io`) on the source object or profile after each reconcile:

| Reason | Type | On | When |
|--------|------|----|------|
| `ReplicationSucceeded` | Normal | Source, profile | At least one replica was created, updated, or deleted |
| `ReplicationSkipped` | Normal | Source | Targets were skipped because of pre-existing unmanaged objects |
| `ReplicationFailed` | Warning | Source, profile | One or more targets could not be written |
| `InvalidSelector` | Warning | Profile | `targetSelector` could not be parsed |

```bash
kubectl get events -n platform --field-selector involvedObject.name=shared-api-token
```

## Audit log

Every create, update, delete, skip, conflict, adoption, expiry, cleanup, and consent denial is logged as a structured `replication audit` line with the fields `action`, `mode`, `sourceKind`, `sourceNamespace`, `sourceName`, `targetNamespace`, and `reason`. Actions are `create`, `update`, `delete`, `skip`, `conflict`, `adopt`, `ttl_expire`, `cleanup`, `consent_denied`, and `force_adopt_disabled`. Log format and field names are not covered by the compatibility promise.

## Run locally (out-of-cluster)

```bash
go run ./cmd/spillway
```

This uses the local kubeconfig and requires cluster-wide permissions equivalent to the provided RBAC.

## Limitations

- Spillway uses cluster-wide watches/lists for `Secrets`, `ConfigMaps`, and `Namespaces`; scope and permissions are cluster-level.
- Cleanup is source-driven; if you uninstall without removing source annotations or `SpillwayProfile` resources first, existing replicas remain.
- Expired TTL replicas are not recreated; see [Replica TTL](annotations.md#replica-ttl).
