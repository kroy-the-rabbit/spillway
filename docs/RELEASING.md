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

1. Update code/docs/chart. If `api/` changed, run `make manifests` and commit the generated files.
2. Make sure `CHANGELOG.md` has an entry under `## [Unreleased]` for every user-facing change.
3. Run `make bump VERSION=X.Y.Z` (without `v`). This is the only supported way to
   change the version: it stamps `charts/spillway/Chart.yaml` (`version`, `appVersion`),
   `config/default/kustomization.yaml` (`newTag`), `README.md`, and `docs/index.html`,
   rolls `[Unreleased]` in `CHANGELOG.md` into a dated `[X.Y.Z]` section, then verifies
   they all agree. CI runs `make check-version` on every push.
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
