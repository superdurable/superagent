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
| `internal/mcp`               | Trusted server config, discovery, policy, single-attempt sessions, brokers     | Agent state transitions, retry loops, or exported Dex resources    |
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

Renewable sandbox credentials are outside `AIAgentFlow`. A separately designed
`SandboxLifecycleFlow` will own that lifecycle. This repository currently
defines no placeholder API or compatibility path for it.

`CurrentMessages` and `ArchivedMessages` are the typed application history;
they are not Dex execution history. `AgentState` owns the retained sequence
range, interaction mode, status, pending tool cursor, plan revision, and
consecutive Plan no-progress count. Plans, pending approvals, timers, input
prompts, and cumulative context summaries are separate typed Attributes.

Snapshot is the only durable current-interaction and reconciliation read model.
Archive paging is an immutable history continuation. Each page uses one
read-only Flow RPC that loads `AgentState` and the retained archive map, then
returns one exact chunk without loading current interaction state or pending
Channels. Dex Go SDK `v0.9.1` fixes selective loads at RPC registration, so an
input-selected AttributeMap instance cannot be loaded independently.

Commands follow Dex's transactional RPC model. There is no permanent command
receipt, caller request ID, payload fingerprint, global mutation revision, or
historical acceptance ledger. A response reports only whether current durable
state accepted the command. After an ambiguous transport result, the caller
reads Snapshot and reconciles current state.

`SendMessage` publishes a validated `PendingUserMessage` directly to
`QueuedUserMessages`. The Agent Client creates its stable application message
ID once before the RPC retry loop. Snapshot returns that ID so edit, delete,
and steer target the exact payload while the Flow resolves the private Dex
Channel envelope. `SteerMessage` preserves the application ID when moving the
payload to `SteeredUserMessages`; a repeated or stale ID is not accepted.

Queued messages use `QueuedUserMessages`. Validated question answers bypass
both message queues. `AnswerQuestions` verifies the exact pending call ID and
every question ID. One RPC commit deletes `PendingUserInput` and publishes the
validated answer to `AnsweredUserInputs`. `AwaitUser` waits only on that Channel
while a question is pending and checks for an already-published answer before
the Attribute. Its Execute appends the answer to `CurrentMessages`, emits a
content-free `user_input_answered` activity, and enters `AnsweredInput`.
`AnsweredInput` starts the answer model turn without checking either message
queue. Steering is otherwise consumed only at explicit safe Step
boundaries, so it cannot claim to cancel an in-flight model or MCP side effect.
`ApproveTool` accepts only the current
`PendingApproval.CallID`; it deletes the pending value and publishes the
decision in the same commit. Repeated and stale commands are rejected.

Plan execution is available only at a durable `waiting_for_message` boundary
for the latest revision with no pending input, approval, timer, queued message,
or steering. The browser derives the button state from Snapshot and transient
input-consumption events. Consumption closes the boundary immediately; only a
later Snapshot taken at a new waiting-input round can reopen it.
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
replacement. They also receive Dex attempt metadata.

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
and Run identity through one reducer action. Snapshot is an application-state
query, not a Dex lifecycle projection. It remains readable from retained closed
Flow history and returns the last durable application view. The browser then
lists the configured recent tail of each Stream, applies those events
chronologically, and long-polls from the newest returned resume token. The browser orders every
observed activity event, reasoning summary, live assistant response, and durable
message in one timeline by creation time. Reasoning entries are keyed by the
producing model invocation source. Completion activity marks later text from
the same source as finalizing instead of starting a second live response. Model
activity carries the target durable message sequence. The browser places each
reasoning summary before that assistant message when timestamps tie or are
unavailable. Unanchored reasoning retains its own chronological position.
`WaitingInputRound` is a monotonic `int64` Attribute bounded by JavaScript's
safe integer maximum. With no pending question, `AwaitUser.WaitFor` increments
it only when steered, queued, and current-Plan execution Channels are empty. A
pending question increments it only when `AnsweredUserInputs` is empty, then
waits exclusively for an answer. The
browser takes the first Snapshot round as a watermark, then long-polls
for `round > watermark`. Each response returns the actual matched round, which
becomes the next watermark before requesting Snapshot.

Every consumed queued message, steered message, or Plan execution request emits
one `input_consumed` Activity event with exact application IDs or revision and
no user content. The reducer removes only
matching visible inputs and closes
the transient `isWaitingForInput` gate. It projects payloads already known from
Snapshot into temporary user bubbles without adding content to the Stream.
Consumed IDs suppress stale queue data until durable history replaces those
bubbles. Replays are idempotent and cannot remove messages that arrived later.
A later authoritative Snapshot at a real waiting boundary reopens the gate.
Stream loss is corrected by Snapshot.

Consumed user bubbles are anchored after the latest explicit message sequence
seen in the Activity stream. This preserves causal ordering while Snapshot is
temporarily behind the Stream.

The approval, manual recovery, and Timer target Steps emit a hidden
`snapshot_required` Activity control from `WaitFor`, after the preceding Step
commits their durable payload. It
requests one non-blocking Snapshot so waits that intentionally do not advance
`WaitingInputRound` remain visible without polling.

Every mutation result immediately requests another Snapshot. Mutation controls
remain disabled until that Snapshot succeeds. Server errors and explicit
reconcile also close this gate. Ordinary Stream events other than explicit
Snapshot controls do not request Snapshot. A visible-page configurable
single-shot freshness timer defaults to 60 seconds and starts only after the
prior Snapshot finishes, so an intervening read resets the complete delay.

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
queue starts expanded as a compact, bounded list. Each message is one truncated
row with inline actions; editing reveals the complete queued text. Agent status
lives inside the fixed composer above its primary action, so it stays visible
without covering the timeline.
Every poll, Snapshot, and command owns cancellation. Snapshot reads are
single-flight and coalesce new triggers into at most one trailing read. A
mutation increments an epoch, so a response started before that mutation cannot
replace newer durable state. Hidden pages release live reads. A visible page
cancels its Stream and Attribute waits before sending a mutation, so durable
commands are not queued behind browser HTTP connection limits. Mutation
dispatch waits until those canceled requests have settled in the browser.
Message send displays one local, non-actionable `Submitting` item. Snapshot
reconciliation replaces it with the queued entry and its stable application
message ID. Failure restores its composer text and plan mode. The composer retains
focus and remains editable while submission and Snapshot reconciliation gate
later mutations. Pending input uses the dedicated
`answerQuestions` operation. The browser
collects every answer locally, permits review, and submits the complete batch.
Preset answers may include a compact supplemental detail that is composed into
the answer string; `Other` requires free text.
HTTP acceptance means the server has durably removed that exact batch and
published the validated answer to `AnsweredUserInputs`. `AwaitUser.Execute`
immediately adds it to chat history and emits `user_input_answered`; the browser
uses that event to display the locally known answer until Snapshot confirms the
durable message. Answers never appear as optimistic queue items. Queue edit, delete, and steer optimistically
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
one exact archive-chunk read, one bounded recent-event read, waiting-input-round
long polling, queue deletion and steering, typed event polling, health, and readiness. Mutation responses
report acceptance without durable command receipts. The API process does not
serve React files. Long-poll expiry has a generated typed body,
so the browser can distinguish normal polling cadence from a transport failure.
Snapshot responses carry the generated `Cache-Control: no-store` contract.
Running responses contain a non-null Agent description. Terminal responses
contain typed Flow status and optional failure metadata with a null description.

The frontend build produces an independent `web/dist` artifact. It reads a
strict `config.json` at page load. The file configures the generated Fetch
client with one API origin and an optional positive
`snapshotRefreshIntervalMilliseconds`, which defaults to `60000`. Changing that
file does not rebuild the bundle. The static host owns browser caching and
Content Security Policy headers.

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

Built-in tool inputs are schema-first contracts under `internal/toolcontract`.
Each provider receives the embedded compact JSON Schema, while Agent execution
uses ogen-generated DTOs, strict decoders, and validators from the same file.
Explicit mappers keep generated contract types out of the Agent domain. MCP
tool schemas remain dynamic runtime data behind `ToolDefinition.InputSchema`.
The public `toolcontract` package exposes the same DTOs, schemas, and strict
decoders to embedding applications such as Superverse.

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
retry or join a bounded parallel wave. `maxParallelToolCalls` defaults to four
and limits each wave. Write operations, approvals, user input, and durable waits
remain sequential barriers. Parallel branches publish typed results without
mutating shared Agent state. One join Step commits final tool messages in the
model's original order.

`ToolDefinition` supplies attempt timeout, maximum attempts, total duration, and
retry-exhaustion policy to Dex StepOptions. The MCP registry performs one call
per Dex attempt and never sleeps or retries internally. Tool-level `isError` is
a completed known failure for the model to correct. A transient or ambiguous
failure returns a Go error. Unknown outcomes and retry exhaustion default to a
durable manual-recovery wait. Trusted per-tool configuration may instead choose
`continue_with_unknown`, which retains the automatic recovery path. Recovery
Snapshots expose only stable call identity, arguments, and error type. They
never expose provider error detail. Every session is closed, stdio subprocesses
are reaped, and idle HTTP connections are closed by the registry owner.

`ResolveToolRecovery` validates one exact recovery revision under the pending
recovery Attribute lock. Resume decisions cover every failed call atomically.
Retries reuse the stable call ID and receive a fresh Dex retry execution.
Stopping or steering records started unknown results, interrupts calls that
were not started, and returns to user input without invoking the model.

Every retry of one logical tool Step reuses its first Attribute snapshot.
Runtime metadata therefore remains stable for the logical call.

## Process lifecycle

`internal/app` owns every long-lived resource. Startup validates configuration,
discovers MCP, constructs providers, opens BlobCache, starts the Worker, waits
for its listener, marks readiness, and then serves the API. The Dex Go SDK `v0.9.1`
Worker negotiates a compatible Server protocol before synchronizing indexes or
binding. Any startup failure closes everything already constructed.

On cancellation or an unexpected Worker/HTTP exit, readiness is cleared. The
HTTP server and Worker receive bounded shutdown contexts, their goroutines are
joined, and the Dex Client, BlobCache, MCP registry, and provider transport are
closed exactly once. No background goroutine is intentionally orphaned.
