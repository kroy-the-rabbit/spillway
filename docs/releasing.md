# Releasing

This page is for maintainers cutting a release. Spillway uses tag-driven semantic versioning releases.

## Versioning scheme

- Git tags: `vMAJOR.MINOR.PATCH`
- Pre-releases: `vMAJOR.MINOR.PATCH-rc.N`
- Container image tags:
    - Always publish the exact chart `appVersion` tag (`MAJOR.MINOR.PATCH`)
    - Also publish the git-tag-compatible alias (`vMAJOR.MINOR.PATCH`) for backward compatibility
    - Stable releases also publish `MAJOR.MINOR`, `MAJOR`, and `latest`
- Helm chart:
    - `charts/spillway/Chart.yaml` `version` = `MAJOR.MINOR.PATCH`
    - `charts/spillway/Chart.yaml` `appVersion` = `MAJOR.MINOR.PATCH`

## Release process

1. Update code/docs/chart. If `api/` changed, run `make manifests` and commit the generated files.
2. Make sure `CHANGELOG.md` has an entry under `## [Unreleased]` for every user-facing change. The [Changelog](changelog.md) page on this site is rendered from that file.
3. Run `make bump VERSION=X.Y.Z` (without `v`). This is the only supported way to change the version: it stamps `charts/spillway/Chart.yaml` (`version`, `appVersion`), `config/default/kustomization.yaml` (`newTag`), `README.md`, and `docs/install.md`, rolls `[Unreleased]` in `CHANGELOG.md` into a dated `[X.Y.Z]` section, then verifies they all agree. CI runs `make check-version` on every push. `docs/install.md` is the only docs page that carries the version literal; keep it that way.
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

## Documentation site

The site at <https://spillway.kroy.io> is built with Material for MkDocs from `docs/` and `mkdocs.yml` by `.github/workflows/deploy-pages.yaml` on every push to `main`. Build it locally with:

```bash
pip install mkdocs-material pymdown-extensions
mkdocs build --strict
```
