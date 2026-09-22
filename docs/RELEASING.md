# Releasing Spillway

Spillway uses tag-driven semantic versioning releases.

## Versioning Scheme

- Git tags: `vMAJOR.MINOR.PATCH` (example: `v0.1.0`)
- Pre-releases: `vMAJOR.MINOR.PATCH-rc.N` (example: `v0.2.0-rc.1`)
- Container image tags:
  - Always publish the exact chart `appVersion` tag (`0.1.0`)
  - Also publish the git-tag-compatible alias (`v0.1.0`) for backward compatibility
  - Stable releases also publish `MAJOR.MINOR`, `MAJOR`, and `latest`
- Helm chart:
  - `charts/spillway/Chart.yaml` `version` = `MAJOR.MINOR.PATCH`
  - `charts/spillway/Chart.yaml` `appVersion` = `MAJOR.MINOR.PATCH`

## Release Process

1. Update code/docs/chart.
2. Set `charts/spillway/Chart.yaml` `version` and `appVersion` to the target version (without `v`).
3. Update version references in `docs/index.html`, `README.md`, and `config/default/kustomization.yaml` (`newTag`).
4. Commit changes.
5. Create a signed tag: `git tag -s vX.Y.Z -m "Release vX.Y.Z"`.
6. Push branch and tag.
7. GitHub Actions publishes:
   - GitHub release artifacts (Linux binaries + checksums)
   - Container image to `ghcr.io/kroy-the-rabbit/spillway`
   - Helm OCI chart to `ghcr.io/kroy-the-rabbit/charts/spillway`

## Validation

- CI runs `gofmt` check, envtest integration tests (real API server via `setup-envtest`; the job fails if they are skipped), `go test ./...`, `go test -race ./...`, `go vet ./...`, binary build smoke, `helm lint`, Helm template smoke render, and Helm install dry-run.
- CI also runs a kind end-to-end job: upgrade from the previous released OCI chart to the local chart (`hack/e2e-upgrade.sh`), then a fresh install exercised by `hack/e2e.sh` (annotation and profile replication, updates, replica recreation, cleanup), then uninstall. Both scripts run locally against any kubeconfig.
- CI also runs `golangci-lint` (errcheck, staticcheck, gocritic, misspell, ineffassign, unused) and a `security` job with `govulncheck` + Trivy filesystem scan (blocks on CRITICAL/HIGH CVEs).
- Release workflow verifies the git tag matches chart `version` and `appVersion`.
