# Python-to-Go migration

## Objective

Port the Dex Python AI agent into a standalone, production-grade Go application
without losing behavior, durability, observability, or frontend capability.
The released Dex Go SDK is the only runtime dependency boundary.

The Python source under `reference/python/` is an immutable parity oracle copied
from Dex OSS commit `13db6da5`.

## Delivery phases

| Phase | Deliverable | Snapshot scope |
|---|---|---|
| 0 | Rules, skill, provenance, hooks, and spike audit | Remove premature reads |
| 1 | Stable Agent core, providers, MCP, command API, runtime, and retained UI | No read HTTP surface |
| 2 | Released Dex Snapshot RPC, OpenAPI, and complete UI reconciliation | One `/snapshot` read |
| 3 | Full reliability, live providers, release, and Python-oracle removal | Final parity and cutover |

## Phase 0

- Preserve the imported Python baseline byte-for-byte.
- Copy and adapt applicable Dex engineering rules.
- Vendor the complete `dex-developer` skill and record its source commit.
- Preserve the pre-plan Go and frontend spike in a recoverable stash.
- Audit every restored file before it can enter a product commit.
- Delete old read-path code, dynamic domain objects, raw state strings, and
  exported Dex resource wrappers.

## Phase 1

Phase 1 was completed on `github.com/superdurable/dex/sdk-go v0.2.9`. It
delivers:

- Typed configuration and domain packages.
- Dex Client, Worker, BlobCache, MCP registry, HTTP server, and graceful
  shutdown.
- A statically visible `AIAgentFlow` Step graph.
- Mock, OpenAI, Anthropic, Gemini, and Groq provider boundaries.
- Durable messages, plans, approval, user input, Timer waits, steering, and
  compaction.
- Queued and steered Channels, `AgentMessages` AttributeMap, sequence metadata,
  buffered assistant/reasoning Streams, and structured activity Stream.
- Command RPCs for message send, steering, tool approval, and plan execution.
- OpenAPI endpoints for portal, start, messages, plan execution, approvals,
  events, health, and readiness.
- A separately deployable React artifact with runtime API-origin configuration
  and generated clients only for the stable Phase 1 surface.

Queue mutation domain behavior may be completed and integration-tested, but its
HTTP endpoints wait for Phase 2 because Snapshot will be the browser's formal
source of queue message IDs.

Phase 1 ends at an explicit external waiting point. It does not begin Snapshot
implementation.

## Intentionally absent in Phase 1

These endpoints are intentionally deferred, not forgotten:

- `GET /products/ai-agent/history`
- `GET /products/ai-agent/message-queue`
- `GET /products/ai-agent/describe`
- `GET /products/ai-agent/status`
- `GET /products/ai-agent/snapshot`
- queue deletion and queue steering HTTP endpoints

The OpenAPI document must omit them. There are no `501` placeholders,
compatibility aliases, temporary aggregators, client-side four-read composition,
or guessed Snapshot SDK signatures.

The underlying `AgentMessages`, queued/steered Channels, and durable Agent state
remain part of Phase 1 Flow behavior.

## Phase 2 entry gates

All conditions must be true:

1. The Dex server Snapshot refactor is merged.
2. A matching Dex Go SDK version is formally released.
3. Dex OSS contains a version-matched runnable AI Agent Snapshot example or
   compile-contract test.
4. AttributeMap, Channel, running Flow, and closed Flow read semantics are
   documented and stable.
5. SDK error types, locks, resource selection, and RPC return types are fixed.
6. The local `dex-developer` skill is refreshed or its version differences are
   recorded and reviewed.

The SDK upgrade is its own commit. It updates `go.mod` and `go.sum`, contains no
local `replace`, compiles all Phase 1 code, runs the complete Dex integration
suite, and records breaking changes before application code changes.

## Phase 2 SDK baseline

The Phase 2 entry gates were reviewed against Dex commit `2f961961` on
2026-09-03. That commit is tagged `sdk-go/v0.2.11` and `cli-v0.1.21`.

- Dex PRs 442 through 444 provide server and client selective RPC state
  loading.
- `sdk-go/integ/rpc_selective_state_test.go` is the version-matched real-server
  compile contract.
- AttributeMap entries and pending Channel messages require explicit RPC load
  selections.
- Pending Channel snapshots preserve FIFO order, message IDs, and values.
- Reading pending messages does not consume them.
- Loading state, transactional execution, and Attribute locking are independent
  controls.
- `StateNotLoadedError` reports access to state omitted from the invocation
  projection.
- Dex PR 445 rejects `/` in AttributeMap and ChannelMap instance keys.

Superagent uses decimal sequence values as `AgentMessages` instance keys. The
slash restriction does not require a data migration.

The vendored `dex-developer` skill is byte-identical at `13db6da5` and
`2f961961`. Its provenance record now identifies the reviewed Phase 2 commit.

## Phase 2 Snapshot contract

The published Dex runnable example and installed SDK are the only signature
sources. The typed application response conceptually contains a run ID,
application history, Agent description, queued user messages, and steered user
messages.

- History comes from `AgentMessages`, never Dex execution history.
- The RPC explicitly selects every required AttributeMap, Channel, and
  description Attribute.
- The RPC is read-only and never consumes a Channel.
- Pagination, terminal state, closed Flow, and missing Flow behavior follow the
  released SDK.
- Dex resource descriptors remain package-private.
- Generated transport types map explicitly to the domain snapshot.

The OpenAPI adds one `GET /products/ai-agent/snapshot` route plus the two queue
mutation routes. The four legacy reads are never introduced.

The browser loads or recovers with one Snapshot, atomically replaces its durable
view through one reducer action, applies events for low-latency deltas, and
re-snapshots after disconnects or sequence gaps.

## Tests

Phase 1 evidence must cover Agent start, messages, plan, approval, user input,
Timer, steering, compaction, Worker replacement, buffered Streams, Stream loss,
MCP transports and cleanup, provider fixtures, stable OpenAPI routes, absence of
deferred routes, the independent UI production artifact, runtime API
configuration, CORS policy, and portal smoke.

The live OpenAI Responses API test loads `OPENAI_API_KEY` from the ignored root
`.env` only through its explicit Make target. It is serial, bounded, and never
prints the key. Default tests remain deterministic.

Phase 2 evidence must cover atomic Snapshot content, application-history
provenance, non-consuming FIFO queues, stable message IDs, concurrency semantics,
pagination and retention, every Flow lifecycle state, cold reconstruction,
typed stale-ID conflicts, one initial browser read, event-gap reconciliation,
refresh recovery, generated contract parity, and `dexcli visualize` diagnostics.

Global gates include generated-code drift, formatting, vet, static analysis,
race tests, fuzzing cadence, vulnerability scanning, strict TypeScript checking,
type-aware lint, component tests, and browser E2E. Failures cannot be hidden by
skips or weakened assertions.

## Documentation

- `ARCHITECTURE.md` describes package ownership and durable/live reconciliation.
- `docs/flow-model.md` distinguishes application history from execution history.
- `docs/adr/0001-wait-for-dex-snapshot.md` records the decision to wait.
- `docs/adr/0002-separate-frontend-deployment.md` records the deployment boundary.
- `CONTRIBUTING.md` documents skill loading, generation, and verification.
- Phase 2's completion report records the Dex server commit, SDK version,
  runnable example source, and test evidence.

## UI/UX

Phase 1 preserves the full React source and styling without connecting the main
conversation page to an incomplete read surface. It retains plans, approvals,
choices, fixed composer, keyboard behavior, and responsive layout.

The frontend and backend are separate deployment units. Frontend configuration
selects the API origin at page load. Frontend releases and rollbacks do not
replace or restart the Go API and Worker process.

Phase 2 uses a single Snapshot reducer action and explicit loading,
reconnecting, stale, terminal, and failure states. Queue mutation uses typed
optimistic updates followed by Snapshot reconciliation. Browser tests assert
zero legacy read requests and exactly one initial Snapshot request.

## Evidence log

| Date | Phase | Evidence |
|---|---|---|
| 2026-09-02 | Baseline | Dex `examples/python` copied from `13db6da5`; 251 tracked files verified byte-for-byte; focused source tests: 16 passed |
| 2026-09-03 | Phase 1 graph | `dexcli visualize` against Dex `13db6da5`: valid graph, 62 nodes, 27 edges, zero diagnostics |
| 2026-09-03 | Phase 1 Dex integration | Real Dex server and Go SDK `v0.2.9`: messages/history provenance, buffered streams, approval/input/timer waits, plan execution, queue/steer semantics, compaction, and Worker replacement passed |
| 2026-09-03 | Phase 1 MCP | Real SDK Streamable HTTP and stdio tests: paginated discovery, headers, execution, bounded read retry/timeout, and subprocess cleanup passed |
| 2026-09-03 | Phase 1 provider | Explicit `.env`-backed OpenAI Responses streaming test passed without exposing credentials |
| 2026-09-03 | Phase 1 safety | Go `1.26.6`, gRPC `v1.82.1`, `govulncheck v1.7.0`, and npm audit report zero known reachable vulnerabilities |
| 2026-09-03 | Phase 1 fuzz | Domain JSON, enum, and built-in tool decoders completed 1.8M+ executions without a failure |
| 2026-09-03 | Phase 1 gates | Governance, generation drift, formatting, binary build, vet, static analysis, unit/race tests, strict TypeScript, ESLint, Vitest, production Webpack, and Playwright passed |
| 2026-09-03 | Deployment boundary | Pure API binary, standalone `web/dist`, runtime origin validation, exact CORS policy, cross-origin E2E, and full repository gates passed |
