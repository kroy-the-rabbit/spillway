# Releasing Spillway

Spillway uses tag-driven semantic versioning releases.

## Versioning Scheme

- Git tags: `vMAJOR.MINOR.PATCH` (example: `v0.1.0`)
- Pre-releases: `vMAJOR.MINOR.PATCH-rc.N` (example: `v0.2.0-rc.1`)
- Container image tags:
  - Always publish the exact tag (`v0.1.0`)
  - Stable releases also publish `vMAJOR.MINOR`, `vMAJOR`, and `latest`
- Helm chart:
  - `charts/spillway/Chart.yaml` `version` = `MAJOR.MINOR.PATCH`
  - `charts/spillway/Chart.yaml` `appVersion` = `MAJOR.MINOR.PATCH`

## Release Process

1. Update code/docs/chart.
2. Set `charts/spillway/Chart.yaml` `version` and `appVersion` to the target version (without `v`).
3. Commit changes.
4. Create a signed tag: `git tag -s vX.Y.Z -m "Release vX.Y.Z"`.
5. Push branch and tag.
6. GitHub Actions publishes:
   - GitHub release artifacts (Linux binaries + checksums)
   - Container image to `ghcr.io/kroy-the-rabbit/spillway`
   - Helm OCI chart to `ghcr.io/kroy-the-rabbit/charts/spillway`

## Validation

- CI runs `gofmt` check, `go test ./...`, `helm lint`, and a Helm template smoke render.
- Release workflow verifies the git tag matches chart `version` and `appVersion`.
