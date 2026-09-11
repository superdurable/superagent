# Contributing to SuperAgent

Read `AGENTS.md` before changing the repository.

## Dex changes

For every turn that modifies or reviews Dex Flow, Step, RPC, resource, Stream,
Timer, retry, or recovery code, load the installed `dex-developer` skill and all
references it routes for that task. Installation instructions are at
https://docs.superdurable.io/build-with-ai/dex-developer-skill. Confirm APIs
against the installed released SDK and a version-matched runnable example or
real-server compile-contract test.

Snapshot and Stream code target Dex Go SDK `v0.6.1`. Recheck the installed SDK source and
the installed skill before changing its resource projection or errors. Never
infer an API from a design screenshot or unreleased branch.

## Deployment boundary

The Go backend and `web/dist` frontend are separate artifacts. Do not add static
assets, filesystem serving, or frontend fallback routes to the backend. Browser
API origins come from `web/public/config.json` at runtime. Cross-origin access
must use the backend's exact origin allowlist.

## Generated contracts

OpenAPI is the HTTP contract source. Change `api/openapi.yaml`, regenerate both
Go and TypeScript output, and run the zero-drift check. Do not edit generated
files or duplicate generated transport models by hand.

```bash
make generate
make check-generated
```

Go code must remain gofmt-clean and pass vet, staticcheck, golangci-lint,
exhaustive enum checking, tests, and the race detector. TypeScript uses the full
strict configuration and type-aware ESLint; handwritten code must not use
`any`. Keep methods for one type together and avoid generic utility packages.

## Tests

Install locked browser dependencies once with `npm --prefix web ci`, then run:

```bash
make governance-check
make format-check
make vet
make lint
make test
make test-public-api
make test-race
make fuzz
make test-web
make vulnerability-check
make audit-web
```

`make check` combines the deterministic, credential-free gates other than the
explicit fuzz cadence. MCP transport tests bind a loopback test server and
launch the test binary as a stdio fixture; restricted build sandboxes may need
permission for those local operations.

With a disposable Dex server running, verify the released SDK boundary and
static graph separately:

```bash
DEX_FLOW_SERVICE_ADDRESS=127.0.0.1:8801 make test-dex-integration
DEX_FLOW_SERVICE_ADDRESS=127.0.0.1:8801 make test-server-integration
DEX_FLOW_SERVICE_ADDRESS=127.0.0.1:8801 make test-full-stack-e2e
DEX_FLOW_SERVICE_ADDRESS=127.0.0.1:8801 make test-integration
make check-flow-definition
make flow-visualize
```

The server integration suite uses a released Dex server, a real Worker, and the
real generated HTTP server. The full-stack suite adds the separately served Web
artifact and asserts visible DOM and interaction results, not only HTTP status.
Neither suite may replace API responses with browser route fulfillment.
Set `SUPERAGENT_E2E_HTTP_ADDRESS`, `SUPERAGENT_E2E_WEB_ADDRESS`, and
`SUPERAGENT_E2E_WORKER_ADDRESS` to isolate a local full-stack run from an
already running development Agent.

The integration suite reads private resources only through Flow RPCs whose
names end in `ForTestOnly`. It must not add an HTTP read endpoint or exported
descriptor getter to make tests easier.

`make check-flow-definition` generates JSON in a temporary directory and fails
on any visualizer diagnostic. `make flow-visualize` analyzes
`internal/agent/flow.go` directly and serves the graph in Flow Rendering.

The explicit live provider test is serial and bounded:

```bash
make test-openai-live
```

It is the only test permitted to read `OPENAI_API_KEY` from the ignored root
`.env`. Never print, stage, or copy that file.

The CI workflow runs deterministic checks, fuzzing, Flow Definition validation,
and real Dex integration. It uploads the Go backend and static frontend as
separate artifacts so either deployment can be released independently.

## npm releases

The `npm-release.yml` workflow publishes `@superdurable/superagent-ui` from a
strict `vX.Y.Z` tag on `main`. The release workflows derive the package version
from the tag in the temporary runner checkout. The committed workspace version
does not require a manual release bump.

The npm package must trust the `superdurable/superagent` GitHub repository with
workflow filename `npm-release.yml`, no environment, and direct `npm publish`
permission. The workflow uses npm trusted publishing and GitHub OIDC. Do not add
an `NPM_TOKEN` secret. A manual workflow dispatch can publish an existing tag
that did not complete automatically.

The `github-release-ui.yml` workflow attaches the matching npm-compatible
archive to every published GitHub Release. Its manual dispatch repairs an
existing Release only when a missing asset is generated, or when the existing
asset is byte-for-byte identical. It never overwrites a different release
artifact. The archive package version and filename both use the release tag.

Before committing, run the full applicable gates and `git diff --check`. Do not
bypass hooks. Inspect the staged diff, commit with a meaningful message, verify
the recorded author/message, and leave a clean worktree.
