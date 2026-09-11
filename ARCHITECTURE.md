# SuperAgent architecture

## System boundary

```text
React portal deployment
    │ runtime-configured generated Fetch client
    ▼
Go API deployment ────── best-effort events
    │ typed command mapping
    ▼
Dex Client ── FlowService ── Dex Worker
                                ├── provider adapter ── model API
                                ├── MCP registry ────── MCP server
                                └── BlobCache (disposable)
```

Dex durable resources are the source of application truth. HTTP handlers map
generated transport types to domain types and invoke commands; they do not own
Agent state. Streams reduce latency but never become recovery state.

## Package ownership

| Package                      | Owns                                                                           | Must not own                                                       |
| ---------------------------- | ------------------------------------------------------------------------------ | ------------------------------------------------------------------ |
| `agent`                      | Public constructors, stable application types, model/tool extension interfaces | Private Dex descriptors, provider protocols, process lifecycle     |
| `model`                      | Public built-in provider adapters, router, and process-memory credentials      | Dex resources, provider protocol implementation, process lifecycle |
| `internal/agent`             | Domain IDs/enums, Flow graph, private Dex descriptors, command client          | Provider protocols, HTTP transport models, global configuration    |
| `internal/api`               | ogen implementation, validation mapping, problem responses                     | Handwritten routes, generated-model duplicates, durable state      |
| `internal/app`               | Dependency construction, goroutine ownership, startup and shutdown             | Domain decisions or provider-specific payloads                     |
| `internal/config`            | Environment parsing and validated immutable sections                           | Runtime singletons or secret logging                               |
| `internal/model`             | Provider routing, protocol adapters, in-memory credential lookup               | Dex resources or HTTP API responses                                |
| `internal/mcp`               | Trusted server config, discovery, policy, sessions, retries, brokers           | Agent state transitions or exported Dex resource access            |
| `web`                        | React portal and generated Fetch client                                        | Handwritten API response types or durable-state reconstruction     |
| `web/packages/superagent-ui` | Transport-free React conversation components and local interaction behavior    | Dex/API clients, routing, durable state, or product workflows      |

Interfaces live at their consuming boundary. Concrete single-use components do
not receive speculative interfaces, and there is no general-purpose helpers
package.

The Web application maps generated domain objects into the plain view models
accepted by `@superdurable/superagent-ui`. The shared package never imports the
generated client or reconstructs application state; this keeps it reusable by
other products without coupling their transport or orchestration to this portal.

The public `agent` package is a thin façade over `internal/agent`. It aliases the
stable application types and delegates constructors, so embedders share the
same Flow implementation as the reference process. It does not expose private
Attribute, Channel, or Stream descriptor handles, nor does it provide generic
reads around them. Provider and tool implementations remain constructor-injected
and may live in the embedding application.

The public `model` package aliases the built-in provider implementations and
delegates their constructors. Embedders can opt into those adapters without
depending on `internal/model` or duplicating provider wiring.

## Durable Agent model

One stable `FlowID` identifies one conversation. `Client.Start` supplies an
immutable `AgentConfig` and optional `RuntimeMetadata`, then starts Dex with
`IDReuseDisallow`. Runtime metadata is one validated JSON object capped at
16 KiB. It exists for trusted integration routing across Worker replacement;
tool implementations receive it with the Flow and call IDs. Models, browser
Snapshots, Streams, and logs do not receive it. It must not contain secrets.

`CurrentMessages` and `ArchivedMessages` are the typed application history;
they are not Dex execution history. `AgentState` owns the retained sequence
range, interaction mode, status, pending tool cursor, plan revision, and
consecutive Plan no-progress count. Plans, pending approvals, timers, input
prompts, and cumulative context summaries are separate typed Attributes.

Snapshot is the only durable current-interaction and reconciliation read model.
Archive paging is an immutable history continuation. Each page uses one
read-only Flow RPC that loads `AgentState` and one exact archive chunk, without
loading current interaction state or pending Channels.

Commands follow Dex's transactional RPC model. There is no permanent command
receipt, caller request ID, payload fingerprint, global mutation revision, or
historical acceptance ledger. A response reports only whether current durable
state accepted the command. After an ambiguous transport result, the caller
reads Snapshot and reconciles current state.

`SendMessage` publishes a validated message directly to
`QueuedUserMessages`. Dex assigns the Channel message ID. Snapshot returns that
ID so edit, delete, and steer target the exact pending entry. `SteerMessage`
loads the queued Channel and atomically deletes that ID before publishing its
value to `SteeredUserMessages`; a repeated or stale ID is not accepted.

Queued messages and validated question answers use `QueuedUserMessages`.
Steering is consumed only at explicit safe Step boundaries, so it cannot claim
to cancel an in-flight model or MCP side effect. `AnswerQuestions` verifies the
exact pending call ID and every question ID. One RPC commit deletes
`PendingUserInput`, publishes the ordered answer message, and writes
`submitted`. `ApproveTool` accepts only the current
`PendingApproval.CallID`; it deletes the pending value and publishes the
decision in the same commit. Repeated and stale commands are rejected.

Plan execution is available only at a durable `waiting_for_message` boundary
for the latest revision with no pending input, approval, timer, queued message,
or steering. The browser derives the button state from Snapshot plus the
interaction-status long poll, so `submitted` closes the boundary immediately.
An executing active Plan that produces no tool call receives one automatic
corrective model turn. A second consecutive no-progress response returns to the
durable wait and exposes `Continue plan`.

Each `WaitFor`, `Execute`, and RPC invocation is an independent Dex atomic commit
boundary. Waiting state is written in the `WaitFor` that establishes the wait.
Provider and MCP calls occur only in `Execute`. The complete graph and resource
table are in `docs/flow-model.md`.

History-reading Steps declare bounded AttributeMap loads explicitly. Integration
tests use verb-first `ForTestOnly` RPCs rather than generic Dex Client resource
operations. Tool invocations receive the stable Flow ID, model call ID, and
runtime metadata as one durable routing identity, including after Worker
replacement.

## Durable and live reconciliation

| Data                                 | Durability                    | Recovery role                |
| ------------------------------------ | ----------------------------- | ---------------------------- |
| Attributes, AttributeMaps, Channels  | Dex durable state             | Authoritative                |
| assistant/reasoning buffered Streams | Best effort                   | UI latency only              |
| structured activity Stream           | Best effort                   | Observability and UI latency |
| BlobCache                            | Disposable local acceleration | Never authoritative          |
| provider/MCP session                 | Per invocation                | Recreated after failure      |
| generated browser state              | In-memory projection          | Replaced from Snapshot       |

The browser performs one generated `GET /products/ai-agent/snapshot` on load and
atomically replaces history, description, queued messages, steered messages,
and Run identity through one reducer action. It then lists the configured recent
tail of each Stream, applies those events chronologically, and long-polls from
the newest returned resume token. The browser orders every
observed activity event, reasoning summary, live assistant response, and durable
message in one timeline by creation time. Reasoning entries are keyed by the
producing model invocation source. Completion activity marks later text from
the same source as finalizing instead of starting a second live response. Model
activity carries the target durable message sequence. The browser places each
reasoning summary before that assistant message when timestamps tie or are
unavailable. Unanchored reasoning retains its own chronological position.
`AgentInteractionStatus` alternates between `submitted` and `waiting`. The
browser long-polls those durable values and reads Snapshot after a real durable
wait. Every mutation result immediately requests another Snapshot. Mutation
controls remain disabled from a waiting boundary or mutation result until that
Snapshot succeeds. Server errors and explicit reconcile also close this gate.
Ordinary Stream events do not request Snapshot. A visible-page ten-second
single-shot freshness timer starts only after the prior Snapshot finishes, so
an intervening read resets the complete delay. Terminal reconciliation stops
Streams, Attribute waits, Snapshot work, and the timer.

Resume tokens belong to the live subscription and are not durable UI state.
Activity events are independent timeline rows keyed by resume token. A page
refresh uses `ListStreamMessages` once per Stream, bounded by
`SUPERAGENT_STREAM_RECOVERY_LIMIT`, which defaults to 1000 and cannot exceed 1000. Events removed by Stream retention or beyond that tail are not
reconstructed. Completed-source tracking prevents recovered text from
duplicating durable assistant messages and keeps recovered reasoning summaries
in a completed state.
The timeline follows new content by default. Only an upward viewport movement
pauses that behavior; composer and queue layout changes keep the latest content
visible. Later message, reasoning, or activity content exposes an explicit
jump-to-latest control instead of moving a reader who is viewing history.
Returning to the bottom resumes following. Archive prepends preserve the
reading position and do not count as new timeline content. The pending-message
queue starts collapsed and changes only when the user toggles it. Agent status
lives inside the fixed composer above its primary action, so it stays visible
without covering the timeline.
Every poll, Snapshot, and command owns cancellation. Snapshot reads are
single-flight and coalesce new triggers into at most one trailing read. A
mutation increments an epoch, so a response started before that mutation cannot
replace newer durable state.
Message send displays one local, non-actionable `Submitting` item. Snapshot
reconciliation replaces it with the queued entry and its Dex-generated message
ID. Failure restores its composer text and plan mode. The composer retains
focus and remains editable while submission and Snapshot reconciliation gate
later mutations. Pending input uses the dedicated
`answerQuestions` operation. The browser
collects every answer locally, permits review, and submits the complete batch.
Preset answers may include a compact supplemental detail that is composed into
the answer string; `Other` requires free text.
HTTP acceptance means the server has durably removed that exact batch and
queued one normal answer message. Queue edit, delete, and steer optimistically
remove one stable message ID.
The backend resolves a steer value from the loaded
Channel snapshot; the browser cannot replace the queued content during that
operation.

## HTTP contract

`api/openapi.yaml` is the sole contract source. ogen generates the Go server,
router, codecs, and validation. Hey API generates the TypeScript Fetch client,
models, and enums. Explicit mappers keep generated transport types out of the
domain package.

The API serves portal metadata, Flow start, command RPCs, one Snapshot read,
one exact archive-chunk read, one bounded recent-event read, interaction-status
long polling, queue deletion and steering, typed event polling, health, and readiness. Mutation responses
report acceptance without durable command receipts. The API process does not
serve React files. Long-poll expiry has a generated typed body,
so the browser can distinguish normal polling cadence from a transport failure.
Snapshot responses carry the generated `Cache-Control: no-store` contract.
Running responses contain a non-null Agent description. Terminal responses
contain typed Flow status and optional failure metadata with a null description.

The frontend build produces an independent `web/dist` artifact. It reads a
strict `config.json` at page load. The file configures the generated Fetch
client with one API origin. Changing that file does not rebuild the bundle. The
static host owns browser caching and Content Security Policy headers.

Continuous integration uploads the Go binary and `web/dist` as separate release
artifacts. Backend rollout never replaces frontend files, and frontend rollout
never restarts the Worker.

Cross-origin API access uses a backend allowlist of exact frontend origins.
Wildcards and credentialed browser requests are unsupported. Plain HTTP is
accepted only for loopback development origins. A same-origin edge proxy can
route both deployments without enabling CORS.

## Providers and credentials

The provider-neutral model boundary accepts typed messages, tools, and callback
writers. OpenAI uses the official Responses streaming SDK. Anthropic, Gemini,
and Groq adapters use strict native protocol structs. Provider redirects are
disabled, response bodies are closed, attempts are bounded by context and HTTP
timeouts, and unknown provider values return typed validation errors.

API keys come from process configuration or an in-memory Flow override. They are
never included in Dex configuration, model messages, activity events, or log
fields. Base URL overrides require trusted absolute HTTPS URLs without embedded
credentials, queries, or fragments.

## MCP safety

MCP server YAML is trusted operator configuration with unknown fields rejected.
stdio commands run directly without a shell and receive a minimal environment.
Streamable HTTP receives only explicitly mapped headers and uses an owned
transport with SDK retries disabled.

Discovery follows every pagination cursor and publishes tools atomically only
after all configured servers succeed. Unknown and write-capable tools require
approval and one attempt by default. Only explicitly trusted read-only tools can
retry; each attempt and total retry duration are bounded. Tool-level `isError`
is a completed known failure and is not blindly retried. Every session is closed,
stdio subprocesses are reaped, and idle HTTP connections are closed by the
registry owner.

## Process lifecycle

`internal/app` owns every long-lived resource. Startup validates configuration,
discovers MCP, constructs providers, opens BlobCache, starts the Worker, waits
for its listener, marks readiness, and then serves the API. Any startup failure
closes everything already constructed.

On cancellation or an unexpected Worker/HTTP exit, readiness is cleared. The
HTTP server and Worker receive bounded shutdown contexts, their goroutines are
joined, and the Dex Client, BlobCache, MCP registry, and provider transport are
closed exactly once. No background goroutine is intentionally orphaned.
