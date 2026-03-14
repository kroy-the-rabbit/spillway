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
| 0.3.x   | Yes       |
| < 0.3   | No        |

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
signed via the release workflow. Verify image digests against the GitHub
release checksums before deploying in sensitive environments.
