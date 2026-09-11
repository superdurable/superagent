.PHONY: audit-web build-api build-web check check-agent-rules check-flow-definition \
	check-generated copyright-check flow-visualize format-check fuzz generate \
	generate-go generate-web governance-check install-dexcli install-osv-scanner install-temporal lint lint-go lint-web lint-workflows \
	test test-agent test-api test-app test-config test-dex-integration test-mcp test-model test-openai-live \
	test-full-stack-e2e test-integration test-public-api test-race test-server-integration test-web vet vulnerability-check

GO_BUILD_CACHE := $(CURDIR)/.cache/go-build
GO_PACKAGES := ./agent/... ./cmd/... ./internal/... ./model/...
DEXCLI_VERSION := v0.6.0
DEXCLI_BINARY := $(CURDIR)/.cache/dexcli-$(DEXCLI_VERSION)
OSV_SCANNER_VERSION := v2.5.1
OSV_SCANNER_BINARY := $(CURDIR)/.cache/osv-scanner-$(OSV_SCANNER_VERSION)
TEMPORAL_VERSION := v1.8.2
TEMPORAL_BINARY := $(CURDIR)/.cache/temporal-$(TEMPORAL_VERSION)/temporal
STATICCHECK_VERSION := v0.7.0
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION := v1.7.0
ACTIONLINT_VERSION := v1.7.12
FUZZ_TIME ?= 10s
INTEGRATION_TEST_RUN ?= ^TestAgent.*Integration$$
INTEGRATION_TEST_TIMEOUT ?= 5m

build-api:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go build -o bin/superagent ./cmd/superagent

build-web:
	@npm --prefix web run build

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

install-dexcli: $(DEXCLI_BINARY)

$(DEXCLI_BINARY): script/install-dexcli.sh
	@sh script/install-dexcli.sh "$(DEXCLI_BINARY)" "$(DEXCLI_VERSION)"

install-osv-scanner: $(OSV_SCANNER_BINARY)

$(OSV_SCANNER_BINARY): script/install-osv-scanner.sh
	@sh script/install-osv-scanner.sh "$(OSV_SCANNER_BINARY)" "$(OSV_SCANNER_VERSION)"

install-temporal: $(TEMPORAL_BINARY)

$(TEMPORAL_BINARY): script/install-temporal.sh
	@sh script/install-temporal.sh "$(TEMPORAL_BINARY)" "$(TEMPORAL_VERSION)"

check-flow-definition: install-dexcli
	@set -eu; \
		flow_definition_tmp="$$(mktemp -d)"; \
		trap 'rm -r "$${flow_definition_tmp}"' EXIT; \
		cd "$(CURDIR)"; \
		flow_definition="$${flow_definition_tmp}/ai-agent.json"; \
		if ! GOCACHE=$(GO_BUILD_CACHE) "$(DEXCLI_BINARY)" visualize internal/agent/flow.go --language go --json \
			--out "$${flow_definition_tmp}/ai-agent"; then \
			test ! -f "$${flow_definition}" || sed -n '/"diagnostics"/,$$p' "$${flow_definition}"; \
			exit 1; \
		fi; \
		if ! grep -Fqx '  "valid": true,' "$${flow_definition}" || \
			! grep -Fqx '  "diagnostics": []' "$${flow_definition}"; then \
			echo "Flow definition must be valid with zero diagnostics" >&2; \
			sed -n '/"diagnostics"/,$$p' "$${flow_definition}" >&2; \
			exit 1; \
		fi; \
		for channel in queuedUserMessagesChannel steeredUserMessagesChannel toolApprovalsChannel planExecutionsChannel; do \
			if ! grep -Fq "\"id\": \"resource:channel:$${channel}\"" "$${flow_definition}" || \
				! grep -Fq "\"resourceId\": \"resource:channel:$${channel}\"" "$${flow_definition}"; then \
				echo "Flow definition must render Channel $${channel} and its WaitFor edge" >&2; \
				exit 1; \
			fi; \
		done

flow-visualize: install-dexcli
	@cd "$(CURDIR)" && GOCACHE=$(GO_BUILD_CACHE) "$(DEXCLI_BINARY)" visualize internal/agent/flow.go --language go

vet:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go vet $(GO_PACKAGES)

lint-go:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) $(GO_PACKAGES)
	@GOCACHE=$(GO_BUILD_CACHE) GOLANGCI_LINT_CACHE=$(CURDIR)/.cache/golangci-lint GOWORK=off \
		go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run $(GO_PACKAGES)

lint-web:
	@npm --prefix web run typecheck
	@npm --prefix web run lint

lint-workflows:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

lint: lint-go lint-web lint-workflows

vulnerability-check:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(GO_PACKAGES)

audit-web: install-osv-scanner
	@"$(OSV_SCANNER_BINARY)" scan source --lockfile=web/package-lock.json

test-agent:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test ./internal/agent

test-public-api:
	@sh script/test-public-api.sh

test-dex-integration:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test -tags=integration -count=1 \
		-run '$(INTEGRATION_TEST_RUN)' -timeout '$(INTEGRATION_TEST_TIMEOUT)' ./internal/agent

test-server-integration:
	@GOCACHE=$(GO_BUILD_CACHE) GOWORK=off go test -p 1 -tags=integration -count=1 \
		-run '$(INTEGRATION_TEST_RUN)' -timeout '$(INTEGRATION_TEST_TIMEOUT)' ./internal/agent ./internal/api

test-full-stack-e2e: build-api build-web
	@sh script/test-full-stack-e2e.sh

test-integration: test-server-integration test-full-stack-e2e

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

test: test-public-api test-agent test-api test-app test-config test-mcp test-model

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

check: governance-check check-generated format-check build-api build-web vet lint test test-race test-web vulnerability-check audit-web
