SHELL := /usr/bin/env bash
VERSION ?=

.PHONY: help build test test-race vet fmt-check lint check-version bump helm-lint e2e generate manifests check-generated

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-16s %s\n", $$1, $$2}'

build: ## Build the controller binary
	go build -ldflags="-s -w" -o bin/spillway ./cmd/spillway

test: ## Unit, smoke and envtest tests (set KUBEBUILDER_ASSETS for envtest)
	go test ./...

test-race: ## Tests with the race detector
	go test -race ./...

vet: ## go vet
	go vet ./...

fmt-check: ## Fail if any Go file is not gofmt-clean
	@test -z "$$(gofmt -l cmd internal api)" || { gofmt -l cmd internal api; exit 1; }

lint: ## golangci-lint (must be installed)
	golangci-lint run ./...

helm-lint: ## helm lint the chart
	helm lint charts/spillway

generate: ## Regenerate DeepCopy methods from api/ types (controller-gen)
	go tool controller-gen object paths=./api/...

manifests: generate ## Regenerate the CRD and the chart's copy of it
	go tool controller-gen crd paths=./api/... output:crd:dir=config/crd
	hack/sync-crd.sh

check-generated: manifests ## Fail if generated code or manifests are stale
	hack/sync-crd.sh --check
	git diff --exit-code -- api/ config/crd/ charts/spillway/templates/crd.yaml

check-version: ## Fail if any version reference disagrees with Chart.yaml
	hack/bump-version.sh --check

bump: ## Stamp a new release version everywhere: make bump VERSION=X.Y.Z
	@test -n "$(VERSION)" || { echo "usage: make bump VERSION=X.Y.Z"; exit 2; }
	hack/bump-version.sh $(VERSION)

e2e: ## Run the kind e2e suite against the current kubeconfig (chart must be installed)
	hack/e2e.sh
