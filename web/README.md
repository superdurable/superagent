# Superagent web application

This React application consumes only the TypeScript client generated from
`api/openapi.yaml`. Do not hand-write HTTP request or response models.

Phase 1 exposes the launch portal and durable Flow start. After start, the UI
enters an explicit Snapshot gate. It does not call the legacy history, describe,
status, or message-queue read endpoints. The complete original frontend remains
unchanged under `reference/python/ai-agent/` as the Phase 2 parity oracle.

## Commands

```bash
npm ci
npm run generate:api
npm run typecheck
npm run lint
npm run build
```

The production build is written to `internal/webui/assets/` and embedded in the
Go binary. Generated files are committed; `make check-generated` verifies that
both the ogen server and Hey API client have zero drift.

Open `http://127.0.0.1:8080/products/ai-agent/` after Dex and Superagent start.

## Phase 2

Once the published Dex Snapshot API satisfies every gate in `MIGRATION.md`, the
conversation UI will be rebuilt against one generated `/snapshot` read plus
generated `/events` long polling. Snapshot will atomically replace durable view
state; events will remain low-latency hints. No legacy four-read compatibility
layer will be introduced.
