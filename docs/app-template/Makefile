# The one place commands are defined. CI calls these targets; so do people.
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

MODULE     = $(shell go list -m)
VERSION    ?= $(shell git describe --tags --exact-match 2>/dev/null || git rev-parse --short=8 HEAD 2>/dev/null || echo dev)
LDFLAGS    = -s -w -X $(MODULE)/pkg/buildinfo.Version=$(VERSION)
IMAGE_REPO ?= myapp

.PHONY: help build run cli migrate seed dev-up verify lint lint-api test \
        test-integration test-integration-ci e2e vuln docker

help:                ## list the targets
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-20s %s\n", $$1, $$2}'

build:               ## build bin/app and bin/cli
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/app ./cmd/cli

run:                 ## run the server against .env
	go run ./cmd/app

cli:                 ## run a console command: make cli ARGS="db:migrate up"
	go run ./cmd/cli $(ARGS)

migrate:             ## apply pending migrations
	go run ./cmd/cli db:migrate up

seed:                ## run pending seeders
	go run ./cmd/cli db:seed

dev-up:              ## start the local database from compose.yaml
	docker compose up -d --wait postgres

verify:              ## formatting, vet (including tagged tests), module tidiness
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt -w needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go vet -tags=integration,e2e ./...
	go mod tidy -diff

lint:                ## golangci-lint, at the version CI uses
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run

lint-api:            ## lint api/openapi.yaml against redocly.yaml (needs Node)
	npx --yes @redocly/cli@2.54.3 lint api/openapi.yaml

test:                ## unit tier, with the race detector
	go test -race -count=1 ./...

test-integration:    ## integration tier against a fresh local database on :5433
	docker compose up -d --wait --force-recreate postgres-test
	trap 'docker compose rm -sfv postgres-test >/dev/null' EXIT; $(MAKE) test-integration-ci

test-integration-ci: ## integration tier against the database GORGANY_TEST_* names (CI)
	go test -tags=integration -count=1 -p 1 ./...

e2e:                 ## e2e tier against the production image
	test/e2e/run.sh

vuln:                ## known vulnerabilities in reachable code
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

docker:              ## build the production image, tagged with VERSION
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE_REPO):$(VERSION) .
