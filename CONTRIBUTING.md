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

## Verification

Run the documented Make targets. Default suites must be deterministic and must
not need credentials. The explicit live OpenAI target is the only test allowed
to load the ignored root `.env`.

Before committing, run `make governance-check`. Do not bypass hooks. After the
commit, inspect the author and message and leave a clean worktree.
