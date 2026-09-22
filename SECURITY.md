# Security Policy

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Report security issues privately via GitHub's [Security Advisories](../../security/advisories/new)
or by emailing the maintainer directly (see profile contact).

You will receive an acknowledgement within 48 hours and a resolution timeline
within 7 days of triage.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.5.x   | Yes       |
| 0.4.x   | Yes       |
| < 0.4   | No        |

## Security Considerations

### Cluster-wide RBAC scope

spillway requires a `ClusterRole` with cluster-wide read access to
`Secrets`, `ConfigMaps`, `Namespaces`, and `SpillwayProfiles`, plus
cluster-wide write access to replicated `Secrets` and `ConfigMaps`,
`spillwayprofiles/status`, `spillwayprofiles/finalizers`, and `Leases`
for leader election. This is by design — the controller must be able to
read source objects and write replicas in any namespace.

**Recommendation:** Review the ClusterRole carefully before deploying and
restrict `spillway.kroy.io/replicate-to` and
`spillway.kroy.io/replicate-to-matching` annotations to trusted namespace
owners via admission policy (e.g. Kyverno or OPA Gatekeeper).

### Secrets readable by the controller

The controller reads the full contents of annotated `Secret` objects in
order to replicate them. Any process or service account that can impersonate
the spillway controller service account, or can read its leader-election
lease, gains indirect access to those secrets.

**Recommendation:** Follow least-privilege for the controller's ServiceAccount
and restrict which workloads can annotate Secrets in your cluster.

### Network exposure

The metrics endpoint (`:8080`) exposes Prometheus counters with replication
statistics. No secret data is exposed. The default Helm NetworkPolicy limits
metrics ingress to pods in the release namespace; add explicit
`networkPolicy.ingress` rules if your scraper runs elsewhere.

### Supply chain

Container images are published to `ghcr.io/kroy-the-rabbit/spillway` and
the Helm chart to `oci://ghcr.io/kroy-the-rabbit/charts/spillway`. Every
release is signed keylessly with [Sigstore cosign](https://github.com/sigstore/cosign)
using the GitHub Actions OIDC identity of the release workflow, and ships
SPDX SBOMs. Verification requires cosign v3.0 or newer (signatures use the
Sigstore bundle format).

The signing identity for a release tag `vX.Y.Z` is
`https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/vX.Y.Z`
issued by `https://token.actions.githubusercontent.com`.

Verify the container image (replace `<version>` with e.g. `0.5.1`):

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

Verify the binary checksums (`checksums.txt` and `checksums.txt.sigstore.json`
are attached to the GitHub release), then check the tarballs against them:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

Verify the image SBOM attestations (one SPDX SBOM per platform is attested
to the multi-arch image; the same SBOMs plus a source-tree SBOM are attached
to the GitHub release as `spillway_<version>_*_sbom.spdx.json`):

```sh
cosign verify-attestation --type spdxjson \
  --certificate-identity-regexp '^https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/kroy-the-rabbit/spillway:<version>
```

To pin to exactly one release, replace `--certificate-identity-regexp` with
`--certificate-identity https://github.com/kroy-the-rabbit/spillway/.github/workflows/release.yaml@refs/tags/v<version>`.
Pin deployments to an image digest rather than `latest` in sensitive environments.

GitHub Actions workflows are pinned to commit SHAs to prevent silent
supply chain updates.

### Authorization model

By default, any namespace can receive replicas from any source
(allow-by-default). For environments that require explicit namespace
opt-in, run the controller with `--require-namespace-consent`. Namespaces
must then set the `spillway.kroy.io/accept-from` annotation to receive
replicas.

Protected namespaces (default: `kube-system`) cannot be targeted by
wildcard or label-selector replication unless named explicitly.
Use `--protected-namespaces` to extend this list.

The `force-adopt` annotation is disabled by default. Enable it only with
`--allow-force-adopt` and after reviewing admission policies to limit who
may set this annotation.
