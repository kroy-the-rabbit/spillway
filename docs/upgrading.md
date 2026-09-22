# Upgrading

This page covers the move to 1.0 and what you can rely on across the 1.x line.

## Upgrading to 1.0

1.0 changes nothing about how replication works. The upgrade is the CRD version:

1. Upgrade the chart as usual (`helm upgrade ... --version "${VERSION}"` with the 1.0 release version). The chart installs the CRD with both `v1` (storage) and `v1alpha1` (served, deprecated). Existing `SpillwayProfile` objects keep working; the API server converts between the two versions without a webhook because the schemas are identical.
2. Update your manifests and GitOps sources from `apiVersion: spillway.kroy.io/v1alpha1` to `spillway.kroy.io/v1`. Until you do, `kubectl` prints a deprecation warning on every apply.
3. Migrate stored objects so `v1alpha1` can be dropped later:
   ```bash
   hack/migrate-storage-version.sh
   ```
   It rewrites every profile at the `v1` storage version and sets the CRD's `status.storedVersions` to `["v1"]`. It is idempotent.

Rolling back to 0.6.x after step 3 requires re-adding `v1alpha1` to the CRD, so run step 3 once you are satisfied with 1.0.

## Compatibility promise (1.x)

From the 1.0 release, the following are stable for the whole 1.x line. Changes to them are additive only; anything removed goes through at least one minor release of deprecation warnings first.

- **Annotations** under `spillway.kroy.io/` listed in the [annotation reference](annotations.md#reference): names, value syntax, and semantics.
- **`SpillwayProfile` `spillway.kroy.io/v1`**: the schema and reconciliation semantics. `v1alpha1` is served and marked deprecated; it is removed no earlier than 1.2.
- **Controller flags** as printed by `spillway --help`: names, defaults, and meaning.
- **Helm values** in `charts/spillway/values.yaml`: keys and defaults.
- **Replica markers**: the `spillway.kroy.io/managed-by` label and the `managed-by`, `source-from`, `profile-ref`, and `expires-at` annotations on replicas. Tooling may depend on them.
- **Metrics** listed under [Metrics](operations.md#metrics): names and label sets.

Not covered by the promise:

- Log line format and field names.
- Kubernetes Event reasons and messages.
- The internal Go packages (`internal/`, `cmd/`). `api/v1` is importable and covered.
- Exact reconcile timing, requeue intervals, and ordering across namespaces.
- The Kustomize manifests under `config/`, which track the chart but are a convenience for development.

## Versioning

- Version tags: `vMAJOR.MINOR.PATCH`
- Pre-releases: `vMAJOR.MINOR.PATCH-rc.N`
- Release tags trigger GitHub Actions automation for binaries, container image, Helm OCI chart, and GitHub release assets.
- `charts/spillway/Chart.yaml` `version` and `appVersion` match the release version (without leading `v`).

See [Releasing](releasing.md) for the maintainer process.
