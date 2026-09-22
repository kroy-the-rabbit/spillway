# Security

This page explains what the controller can read and write, how to limit who can trigger replication, and how to verify that a release came from this project's CI.

To report a vulnerability, use GitHub [Security Advisories](https://github.com/kroy-the-rabbit/spillway/security/advisories/new) rather than a public issue. See [`SECURITY.md`](https://github.com/kroy-the-rabbit/spillway/blob/main/SECURITY.md) for the response timeline and supported versions.

## Cluster-wide RBAC scope

Spillway requires a `ClusterRole` with cluster-wide read access to `Secrets`, `ConfigMaps`, `Namespaces`, and `SpillwayProfiles`, plus cluster-wide write access to replicated `Secrets` and `ConfigMaps`, `spillwayprofiles/status`, `spillwayprofiles/finalizers`, and `Leases` for leader election. This is by design: the controller must be able to read source objects and write replicas in any namespace.

The controller reads the full contents of annotated `Secret` objects in order to replicate them. Any process or service account that can impersonate the Spillway controller service account, or can read its leader-election lease, gains indirect access to those secrets.

Recommendations:

- Review the ClusterRole before deploying.
- Follow least privilege for the controller's ServiceAccount.
- Restrict `spillway.kroy.io/replicate-to`, `spillway.kroy.io/replicate-to-matching`, and `SpillwayProfile` creation to trusted principals via admission policy (see below).

## Authorization model

### Namespace consent

By default, any namespace can receive replicas from any source (allow-by-default). For environments that require explicit namespace opt-in, run the controller with `--require-namespace-consent`:

```bash
helm upgrade spillway oci://ghcr.io/kroy-the-rabbit/charts/spillway \
  --namespace spillway-system \
  --reuse-values \
  --set controller.requireNamespaceConsent=true
```

Namespaces must then set `spillway.kroy.io/accept-from` to receive replicas:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: tenant-b
  annotations:
    spillway.kroy.io/accept-from: "*/platform/*"
```

The value syntax is documented under [Namespace consent](annotations.md#namespace-consent).

### Protected namespaces

Protected namespaces (default: `kube-system`) cannot be targeted by wildcard or label-selector replication unless named explicitly. Use `--protected-namespaces` to extend this list:

```yaml
controller:
  protectedNamespaces: "kube-system,kube-public,cert-manager"
```

### Force-adopt

The `force-adopt` annotation is disabled by default. Enable it only with `--allow-force-adopt` and after reviewing admission policies to limit who may set this annotation.

```yaml
controller:
  allowForceAdopt: true
```

### Annotation filtering

Spillway never copies `kubectl.kubernetes.io/`, `helm.sh/`, `argocd.argoproj.io/`, and other controller-specific annotation prefixes to replicas. The full default list and the override are in [Operations](operations.md#annotation-filtering).

## Admission policies

Anyone who can create or update a Secret or ConfigMap can trigger cross-namespace replication unless you restrict the annotations. The repository ships Kyverno `ClusterPolicy` examples under [`examples/admission-policies/kyverno/`](https://github.com/kroy-the-rabbit/spillway/tree/main/examples/admission-policies/kyverno). Adjust the `clusterRoles` names to match your RBAC model.

=== "Restrict replication annotations"

    Only subjects with the `spillway-source` ClusterRole may set `replicate-to` or `replicate-to-matching`; only `spillway-admin` may set `force-adopt`.

    ```yaml
    apiVersion: kyverno.io/v1
    kind: ClusterPolicy
    metadata:
      name: restrict-spillway-replication-annotations
    spec:
      validationFailureAction: Enforce
      background: false
      rules:
        - name: restrict-replicate-to
          match:
            any:
              - resources:
                  kinds:
                    - Secret
                    - ConfigMap
                  operations:
                    - CREATE
                    - UPDATE
          preconditions:
            any:
              - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/replicate-to\" || '' }}"
                operator: NotEquals
                value: ""
              - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/replicate-to-matching\" || '' }}"
                operator: NotEquals
                value: ""
          deny:
            conditions:
              all:
                - key: "spillway-source"
                  operator: AnyNotIn
                  value: "{{ request.userInfo.clusterRoles }}"

        - name: restrict-force-adopt
          match:
            any:
              - resources:
                  kinds:
                    - Secret
                    - ConfigMap
                  operations:
                    - CREATE
                    - UPDATE
          preconditions:
            any:
              - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/force-adopt\" || '' }}"
                operator: NotEquals
                value: ""
          deny:
            conditions:
              all:
                - key: "spillway-admin"
                  operator: AnyNotIn
                  value: "{{ request.userInfo.clusterRoles }}"
    ```

=== "Restrict SpillwayProfile creation"

    Profiles can replicate to any namespace they select, so treat them as privileged.

    ```yaml
    apiVersion: kyverno.io/v1
    kind: ClusterPolicy
    metadata:
      name: restrict-spillwayprofile-create
    spec:
      validationFailureAction: Enforce
      background: false
      rules:
        - name: require-platform-admin
          match:
            any:
              - resources:
                  kinds:
                    - SpillwayProfile
                  operations:
                    - CREATE
                    - UPDATE
          deny:
            conditions:
              all:
                - key: "platform-admin"
                  operator: AnyNotIn
                  value: "{{ request.userInfo.clusterRoles }}"
    ```

=== "Block wildcard targets"

    Non-admins may name namespaces explicitly but may not use `all` or an empty selector.

    ```yaml
    apiVersion: kyverno.io/v1
    kind: ClusterPolicy
    metadata:
      name: restrict-spillway-wildcard-targets
    spec:
      validationFailureAction: Enforce
      background: false
      rules:
        - name: deny-wildcard-replicate-to
          match:
            any:
              - resources:
                  kinds:
                    - Secret
                    - ConfigMap
                  operations:
                    - CREATE
                    - UPDATE
          preconditions:
            any:
              - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/replicate-to\" || '' }}"
                operator: NotEquals
                value: ""
          deny:
            conditions:
              all:
                - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/replicate-to\" }}"
                  operator: AnyIn
                  value:
                    - "all"
                    - "All"
                    - "ALL"
                - key: "spillway-admin"
                  operator: AnyNotIn
                  value: "{{ request.userInfo.clusterRoles }}"

        - name: deny-wildcard-replicate-to-matching
          match:
            any:
              - resources:
                  kinds:
                    - Secret
                    - ConfigMap
                  operations:
                    - CREATE
                    - UPDATE
          preconditions:
            any:
              - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/replicate-to-matching\" || '' }}"
                operator: NotEquals
                value: ""
          deny:
            conditions:
              all:
                - key: "{{ request.object.metadata.annotations.\"spillway.kroy.io/replicate-to-matching\" }}"
                  operator: Equals
                  value: ""
                - key: "spillway-admin"
                  operator: AnyNotIn
                  value: "{{ request.userInfo.clusterRoles }}"
    ```

## Network exposure

The metrics endpoint (`:8080`) exposes Prometheus counters with replication statistics. No secret data is exposed. The default Helm NetworkPolicy limits metrics ingress to pods in the release namespace; add explicit `networkPolicy.ingress` rules if your scraper runs elsewhere.

## Supply chain

Container images are published to `ghcr.io/kroy-the-rabbit/spillway` and the Helm chart to `oci://ghcr.io/kroy-the-rabbit/charts/spillway`. Every release is signed keylessly with [Sigstore cosign](https://github.com/sigstore/cosign) using the GitHub Actions OIDC identity of the release workflow, and ships SPDX SBOMs. Verification requires cosign v3.0 or newer (signatures use the Sigstore bundle format).

The signing identity for a release tag `vX.Y.Z` is `https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/vX.Y.Z` issued by `https://token.actions.githubusercontent.com`.

In the commands below, replace `<version>` with the release version without the leading `v`.

Verify the container image:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/kroy-the-rabbit/spillway:<version>
```

Verify the Helm chart:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/kroy-the-rabbit/charts/spillway:<version>
```

Verify the binary checksums (`checksums.txt` and `checksums.txt.sigstore.json` are attached to the GitHub release), then check the tarballs against them:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

Verify the image SBOM attestations (one SPDX SBOM per platform is attested to the multi-arch image; the same SBOMs plus a source-tree SBOM are attached to the GitHub release as `spillway_<version>_*_sbom.spdx.json`):

```sh
cosign verify-attestation --type spdxjson \
  --certificate-identity-regexp '^https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/kroy-the-rabbit/spillway:<version>
```

To pin to exactly one release, replace `--certificate-identity-regexp` with `--certificate-identity https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v<version>`. Pin deployments to an image digest rather than `latest` in sensitive environments.

GitHub Actions workflows are pinned to commit SHAs to prevent silent supply chain updates.
