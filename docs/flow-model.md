# AI Agent Flow model

## Identity and lifecycle

- Flow type: `AIAgentFlow`
- Business identity: one stable Flow ID per durable Agent conversation
- Start input: typed `AgentConfig`; `EnsureStarted` atomically persists opaque
  application context, global start identity, command receipt, and an optional
  idempotent first message
- Completion: intentionally open-ended; the Agent waits for the next user
  command after each turn
- Command RPCs: `ConfirmStart`, `SendMessage`, `AnswerQuestions`,
  `SteerMessage`, `DeleteQueuedMessage`, `ApproveTool`, `ExecutePlan`, and
  `AcceptCancellation`
- Read RPC: `Snapshot`
- Browser synchronization Attribute: `AgentInteractionStatus`

Each `WaitFor`, `Execute`, and RPC invocation is an independent Dex atomic
commit. Provider and MCP calls are external effects and are not part of a Dex
transaction.

## Step graph

```text
Init
  -> AwaitUser
       -> CheckSteered -> CompactContext? -> CallModel
       -> approved plan execution -----------^

CallModel
  -> CheckSteered -> AwaitUser                 (assistant response)
  -> CheckSteered -> RouteTool                 (tool calls)

RouteTool
  -> CheckSteered -> AwaitToolApproval         (untrusted write)
  -> CheckSteered -> ExecuteTool               (approved/read-only MCP)
  -> CheckSteered -> DurableWait               (timer tool)
  -> AwaitUser                                  (durable input tool)
  -> next tool or CompactContext               (built-in/result)

AwaitToolApproval
  -> CheckSteered -> ExecuteTool               (approved)
  -> next tool or CompactContext               (rejected)
  -> CompactContext                            (steered)

ExecuteTool
  -> next tool or CompactContext

DurableWait
  -> next tool or CompactContext               (timer fired)
  -> CompactContext                            (steered)
```

`CheckSteered` is the only safe-boundary router. It never cancels an in-flight
model or MCP call. A steered message clears stale approval, timer, and pending
input state, persists cancellation results for abandoned calls, enters
application history, and makes the model replan.

## Step responsibilities

| Step                | `WaitFor`                                                    | `Execute` and transition                                                                                          |
| ------------------- | ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------- |
| `Init`              | none                                                         | Validate config, atomically commit bootstrap identity/receipt/message and state, then `AwaitUser`                  |
| `AwaitUser`         | steered batch, one queued message, or current plan execution when no question is pending | Persist waiting status beside the wait; prioritize steering and consume one selected durable command |
| `CompactContext`    | none                                                         | Call the summary provider, commit covered range and summary, then trim only already summarized retained messages  |
| `CallModel`         | none                                                         | Rebuild provider-neutral context, stream buffered deltas, commit the complete assistant message and pending calls |
| `CheckSteered`      | bounded steered batch                                        | Apply steering at a safe boundary or route the explicit continuation                                              |
| `RouteTool`         | none                                                         | Validate built-in arguments and select approval, MCP execution, timer, input, or next-call path                   |
| `AwaitToolApproval` | exact call-ID approval or steering                           | Persist waiting status beside the wait; consume one decision or replan                                            |
| `ExecuteTool`       | none                                                         | Perform one external MCP effect with stable call ID and bounded policy, then persist its result                   |
| `DurableWait`       | Timer or steering                                            | Persist waiting status beside the wait; record completion/interruption and continue                               |

## Durable resources

| Resource                 | Kind            | Purpose                                                                     |
| ------------------------ | --------------- | --------------------------------------------------------------------------- |
| `AgentConfig`            | Attribute       | Immutable execution configuration                                           |
| `ApplicationContext`     | Attribute       | Opaque trusted-application routing context; never model or browser context  |
| `AgentInitialized`       | Attribute       | Initialization boundary used by `EnsureStarted` replay                      |
| `AgentStartIdentity`     | Attribute       | Versioned fingerprint binding Flow ID to config, context, and first message |
| `AgentBootstrap`         | Attribute       | Start-only envelope deleted in the atomic `Init` commit                     |
| `AgentState`             | Attribute       | Sequence range, mode, status, plan revision, and pending-call cursor        |
| `AgentInteractionStatus` | Attribute       | Durable `submitted`/`waiting` browser synchronization boundary              |
| `ContextSummary`         | Attribute       | Cumulative summary and explicit covered sequence                            |
| `CurrentMessages`        | AttributeMap    | Recent provider-neutral messages keyed by sequence                          |
| `ArchivedMessages`       | AttributeMap    | Ten-message chunks keyed by first sequence                                  |
| `AcceptedUserMessages`   | AttributeMap    | Message ID, payload fingerprint, and first acceptance time; no content copy |
| `DurableCommands`        | AttributeMap    | Exact replay outcome keyed by command and caller request ID                 |
| `AcceptedToolApprovals`  | AttributeMap    | Per-call decision fence and first acceptance time                           |
| `AcceptedPlanExecutions` | AttributeMap    | Per-revision execution fence and first acceptance time                      |
| `AgentPlan`              | Attribute       | Atomically replaced short plan                                              |
| `PendingApproval`        | Attribute       | Reloadable approval request                                                 |
| `PendingTimer`           | Attribute       | Reloadable durable wait description                                         |
| `PendingUserInput`       | Attribute       | Reloadable batch of one to three structured questions                       |
| `QueuedUserMessages`     | Channel         | FIFO messages that do not interrupt active work                             |
| `SteeredUserMessages`    | Channel         | Messages consumed only at safe boundaries                                   |
| `ToolApprovals`          | ChannelMap      | Approval decision partitioned by call ID                                    |
| `PlanExecutions`         | ChannelMap      | Execution request partitioned by plan revision                              |
| `ReasoningSummary`       | buffered Stream | Provider-authored reasoning summaries only                                  |
| `AssistantText`          | buffered Stream | Visible response deltas                                                     |
| `AgentActivity`          | Stream          | Bounded single-line lifecycle and Plan task events                          |

Channels are delivery mechanisms, not storage. A queued message enters
application history only after a Step consumes it. Stream loss never changes
durable truth.

`SendMessage` rejects while `PendingUserInput` exists. `AnswerQuestions` locks
that Attribute and requires exactly one non-empty answer for each current
question ID. It deletes the batch, publishes one ordered `UserMessage` to
`QueuedUserMessages`, and writes `submitted` in one RPC commit. The message
retains the answered call ID internally so an active Plan resumes execution.
A stale, duplicate, partial, or mismatched batch commits no changes. Later model
turns may create further batches after the current batch resolves.

Each observed `AgentActivity` event is a separate transient timeline row. The
Stream may also carry revision-scoped Plan task status hints. Activity is never
written to an Attribute, Snapshot, or archive. Reload may replay only events
still retained by Dex and does not guarantee a complete activity history.

## Application history

Application history is `CurrentMessages`, `ArchivedMessages`, and range metadata
in `AgentState`. It is not Dex execution history.

Sequence keys are fixed-width monotonic values. Every committed message has a
stable application message ID. A user message keeps its first durable
acceptance timestamp when it moves from a Channel into history. Assistant and
tool message IDs are deterministic from Flow ID, Run ID, and sequence. Context
reconstruction reads known keys from the retained range and does not enumerate an unbounded map.
Compaction commits a cumulative summary with its exact covered sequence before
deleting messages.

Dex loads ordinary Attributes automatically, but not AttributeMap instances.
Steps that append history explicitly load the bounded `CurrentMessages` map.
`CompactContext`, `CallModel`, and `CheckSteered` also load
`ArchivedMessages` because they can reconstruct or measure retained context.
`WaitFor` handlers do not load either map; their independent snapshots only
need ordinary Attributes and Channel conditions.

`Snapshot` explicitly loads all `CurrentMessages` entries and the pending values
of `QueuedUserMessages` and `SteeredUserMessages`. Ordinary Attributes used for
the description are available under the released RPC semantics. Loading is
independent from locking and transactional execution: this RPC is read-only,
does not lock the resources, and does not consume either Channel. Its history
page never includes archives. At twenty current messages, the oldest ten move
atomically to one `ArchivedMessages` instance. The archive endpoint reads one
exact chunk by sequence and top scrolling requests only the adjacent chunk.

Retention is at least twenty and a multiple of ten. A complete archive chunk is
deleted only after the cumulative compaction summary covers its full range.

`Client.MessagesAfter` reads a bounded ascending page from the canonical
current and archived maps. Its exclusive cursor, last-sequence watermark, and
first-retained sequence let an adapter implement forward pagination and detect
retention gaps. A page contains at most 200 messages. A concurrent archive move
is retried through the immutable archive chunk; a concurrent retention trim
returns a typed missing-sequence error instead of reconstructing from Dex
execution history or a Stream.

The RPC returns the invocation Run ID from Dex context. Consecutive reads retain
Channel FIFO order, values, and stable message IDs. Closed Flows follow the SDK
terminal result and visibility contracts. A terminal Snapshot contains the
matching Run ID, typed lifecycle and failure metadata, and an empty durable
Agent view. This prevents a just-closed Flow's final active RPC projection from
being rendered as current state.

## Retry and failure policy

- `EnsureStarted` uses Dex's released `AlreadyStarted.IgnoreError` behavior.
  The starting Step commits global identity, start receipt, optional message
  ledger, and optional queue publish atomically before its initialization
  marker. The identity covers config, application context, and whether and what
  initial message exists, but excludes the retry request ID.
- Every valid mutation stores its payload fingerprint and outcome under
  `(command, request ID)`. Equal retries replay the first timestamp and outcome;
  unequal retries return a typed conflict. A retry after Flow closure performs
  a read-only ledger reconciliation and succeeds only for an existing exact
  record.
- Message IDs have an independent durable ledger. Reusing one with equal
  content and mode is a replay even under a new request ID; different content
  is a typed conflict.
- Approval effects are fenced by call ID. Plan-execution effects are fenced by
  revision. Concurrent request IDs can therefore commit at most one Channel
  effect; equivalent later requests replay the effect timestamp.
- An active pre-identity Flow can adopt an identity only from an exact start
  request already recorded by the preceding lifecycle implementation. That
  committed request stays read-only replayable after closure. Requests without
  the old durable record are rejected instead of inferring missing identity. A
  pre-identity run reaching `Init` for the first time fails rather than assuming
  that its old caller omitted an initial message.
- `Client.Cancel` first records cancellation acceptance, then invokes released
  Dex 0.4 `StopFlow`, and returns only after observing terminal `canceled`.
  Dex 0.4 does not accept a caller request ID on `StopFlow`; the preceding
  durable command record makes a crash between those operations safely
  retryable. A different request against a terminal Flow returns a typed
  terminal error and never fabricates success.
- Model calls have bounded attempts, total duration, method timeout, and a
  heartbeat timeout sized for expected provider silence.
- Read-only MCP tools may retry within an explicit budget.
- Write or unknown MCP tools require approval and default to one attempt.
- Stable Flow and call IDs plus opaque application context are loaded from Dex
  and passed together through the tool boundary. An integration can recover
  sandbox or workspace routing and derive one idempotency key across retries
  and Worker replacement.
- A timeout after an unprotected write records an unknown outcome; it never
  claims success or a known failure.
- Failed durable commits retry without exposing staged Attribute or Channel
  changes.
- BlobCache and Streams may disappear. A replacement Worker reconstructs all
  required state from Dex.
