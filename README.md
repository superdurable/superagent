# Superagent

Superagent is a production AI agent application built on Dex durable execution.
It is being migrated from the upstream Python implementation to Go using the
released Dex Go SDK while retaining the React interface.

The immutable Python baseline lives in `reference/python/`. Migration phases,
external gates, and verification evidence are tracked in `MIGRATION.md`.

Development rules are in `AGENTS.md`. Dex changes additionally require the
vendored `skills/dex-developer` skill and its routed references.
