# Contributing to Superagent

Read `AGENTS.md` before changing the repository.

## Dex changes

For every turn that modifies or reviews Dex Flow, Step, RPC, resource, Stream,
Timer, retry, or recovery code, read `skills/dex-developer/SKILL.md` and all
references it routes for that task. Confirm APIs against the installed released
SDK and a version-matched runnable Dex example.

Do not implement Snapshot behavior until every Phase 2 gate in `MIGRATION.md`
is satisfied. Never infer an API from the design screenshot.

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
DEX_REPO=/absolute/path/to/dex make flow-visualize
```

The integration suite reads private resources through the Dex Client only. It
must not add an HTTP read endpoint or exported descriptor getter to make tests
easier.

The explicit live provider test is serial and bounded:

```bash
make test-openai-live
```

It is the only test permitted to read `OPENAI_API_KEY` from the ignored root
`.env`. Never print, stage, or copy that file.

Before committing, run the full applicable gates and `git diff --check`. Do not
bypass hooks. Inspect the staged diff, commit with a meaningful message, verify
the recorded author/message, and leave a clean worktree.
