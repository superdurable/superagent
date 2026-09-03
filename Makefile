.PHONY: audit-web build check check-agent-rules check-generated copyright-check flow-visualize \
	format-check fuzz generate generate-go generate-web governance-check lint lint-go lint-web \
	test test-agent test-api test-app test-config test-dex-integration test-mcp test-model test-openai-live \
	test-race test-web test-webui vet vulnerability-check

GO_BUILD_CACHE := $(CURDIR)/.cache/go-build
GO_PACKAGES := ./cmd/... ./internal/...
DEX_REPO ?=
STATICCHECK_VERSION := v0.7.0
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION := v1.7.0
FUZZ_TIME ?= 10s

build:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go build -o bin/superagent ./cmd/superagent

generate: generate-go generate-web

generate-go:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go generate ./internal/api

generate-web:
	@npm --prefix web run generate:api

check-generated:
	@sh script/check-generated.sh

format-check:
	@sh script/check-format.sh

check-agent-rules:
	@sh script/check-agent-rules.sh

copyright-check:
	@sh script/check-license-headers.sh

governance-check: check-agent-rules copyright-check

flow-visualize:
	@test -n "$(DEX_REPO)" || (echo "DEX_REPO must point to a Dex OSS checkout" >&2; exit 2)
	@cd "$(DEX_REPO)/cli" && GOCACHE="$(GO_BUILD_CACHE)" GOWORK=off go run ./cmd/dexcli visualize \
		"$(CURDIR)/internal/agent/flow.go" --language go --json --out "$(CURDIR)/.cache/flow-visualization"

vet:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go vet $(GO_PACKAGES)

lint-go:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) $(GO_PACKAGES)
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run $(GO_PACKAGES)

lint-web:
	@npm --prefix web run typecheck
	@npm --prefix web run lint

lint: lint-go lint-web

vulnerability-check:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(GO_PACKAGES)

audit-web:
	@npm --prefix web audit --audit-level=moderate

test-agent:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/agent

test-dex-integration:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test -tags=integration -count=1 -run '^TestAgentFlowDurabilityIntegration$$' ./internal/agent

test-api:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/api

test-app:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/app

test-config:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/config

test-model:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/model

test-mcp:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/mcp

test-webui:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/webui

test: test-agent test-api test-app test-config test-mcp test-model test-webui

test-race:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test -race $(GO_PACKAGES)

fuzz:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test -run '^$$' -fuzz '^FuzzDomainDecoders$$' -fuzztime=$(FUZZ_TIME) ./internal/agent

test-web:
	@npm --prefix web test
	@npm --prefix web run build
	@npm --prefix web run test:e2e

test-openai-live:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test -tags=live -count=1 -run '^TestLiveOpenAIResponses$$' ./internal/model

check: governance-check check-generated format-check build vet lint test test-race test-web vulnerability-check audit-web
