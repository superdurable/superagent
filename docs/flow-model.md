# AI Agent Flow model

## Identity and lifecycle

- Flow type: `AIAgentFlow`
- Business identity: one stable Flow ID per durable Agent conversation
- Start input: typed `AgentConfig`; trusted `RuntimeMetadata` is an optional
  initial Attribute and must be one JSON object no larger than 16 KiB
- ID reuse: Dex `IDReuseDisallow`
- Completion: intentionally open-ended; the Agent waits for the next user
  command after each turn
- RPCs: `SendMessage`, `AnswerQuestions`, `SteerMessage`, `ApproveTool`,
  `ExecutePlan`, and `Snapshot`
- Browser synchronization Attribute: `AgentInteractionStatus`

Each `WaitFor`, `Execute`, and RPC invocation is an independent Dex atomic
commit. Provider and MCP calls are external effects and are not part of a Dex
transaction.

## Step graph

```text
Init
  -> AwaitUser
       -> CheckSteered -> CompactContext? -> CallModel
       -> accepted plan execution ------------^

CallModel
  -> CheckSteered -> CallModel                 (first active-plan no-progress response)
  -> CheckSteered -> AwaitUser                 (ordinary or repeated no-progress response)
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

| Step | `WaitFor` | `Execute` and transition |
|---|---|---|
| `Init` | none | Validate and persist config/state, then enter `AwaitUser` |
| `AwaitUser` | steering, one queued message, or current plan execution when no question is pending | Persist waiting status beside the wait; prioritize steering and consume one selected command |
| `CompactContext` | none | Call the summary provider, commit the covered range and summary, then trim only summarized retained messages |
| `CallModel` | none | Rebuild context, stream buffered deltas, commit the assistant message and pending calls; retry one active-plan response that made no durable progress |
| `CheckSteered` | bounded steered batch | Apply steering at a safe boundary or route the explicit continuation |
| `RouteTool` | none | Validate built-in arguments and select approval, MCP execution, timer, input, or next-call path |
| `AwaitToolApproval` | exact call-ID approval or steering | Persist waiting status beside the wait; consume one decision or replan |
| `ExecuteTool` | none | Perform one external MCP effect with stable Flow/call identity, then persist its result |
| `DurableWait` | Timer or steering | Persist waiting status beside the wait; record completion or interruption and continue |

## Durable resources

| Resource | Kind | Purpose |
|---|---|---|
| `AgentConfig` | Attribute | Immutable execution configuration |
| `AgentRuntimeMetadata` | Attribute | Trusted runtime routing metadata; never model or browser context |
| `AgentState` | Attribute | Sequence range, mode, status, plan revision, pending-call cursor, and Plan no-progress count |
| `AgentInteractionStatus` | Attribute | Durable `submitted`/`waiting` browser synchronization boundary |
| `ContextSummary` | Attribute | Cumulative summary and explicit covered sequence |
| `CurrentMessages` | AttributeMap | Recent provider-neutral messages keyed by sequence |
| `ArchivedMessages` | AttributeMap | Ten-message chunks keyed by first sequence |
| `AgentPlan` | Attribute | Atomically replaced short plan |
| `PendingApproval` | Attribute | Current reloadable approval request |
| `PendingTimer` | Attribute | Reloadable durable wait description |
| `PendingUserInput` | Attribute | Current batch of one to three structured questions |
| `QueuedUserMessages` | Channel | FIFO messages that do not interrupt active work |
| `SteeredUserMessages` | Channel | Messages consumed only at safe boundaries |
| `ToolApprovals` | ChannelMap | Current decision delivery partitioned by call ID |
| `PlanExecutions` | ChannelMap | Current execution delivery partitioned by plan revision |
| `ReasoningSummary` | buffered Stream | Provider-authored reasoning summaries only |
| `AssistantText` | buffered Stream | Visible response deltas |
| `AgentActivity` | Stream | Bounded lifecycle and Plan task events |

Channels deliver work; they do not store application history. A queued message
enters history only after a Step consumes it. Stream loss never changes durable
truth.

## Command boundaries

Commands carry only the domain fields needed by their current operation. The
Flow stores no request IDs, payload fingerprints, command receipts, global
mutation revision, or historical acceptance records.

`SendMessage` validates one non-empty message and publishes it directly to
`QueuedUserMessages` when no question is pending. The caller does not choose a
message ID. Dex creates the Channel message ID, and Snapshot returns it with the
pending value.

`AnswerQuestions` requires the exact current call ID and exactly one non-empty
answer for every current question ID. The same RPC commit deletes
`PendingUserInput`, publishes one ordered `UserMessage`, and writes
`AgentInteractionStatus=submitted`. A repeated, partial, stale, or mismatched
request is rejected without changing the Flow.

`SteerMessage` accepts only a Dex Channel message ID obtained from Snapshot. A
transactional RPC loads the queued Channel, deletes that exact pending entry,
publishes its value to `SteeredUserMessages`, and writes `submitted`. A repeated
or stale ID returns not found.

`ApproveTool` accepts only `PendingApproval.CallID`. Its RPC deletes the pending
Attribute and publishes the decision atomically. A repeated or stale call ID is
rejected and no historical approval ledger is retained.

`ExecutePlan` accepts only the latest draft or active revision at
`waiting_for_message`. Pending input, approval, timer, queued messages, steering,
or `PendingPlanExecutionRevision` rejects it without publishing. Acceptance sets
that pending revision and publishes the request in one commit. The consuming
Step clears the pending field. Therefore the same still-active revision can be
executed again later with `Continue plan` after two no-progress responses.

Delete removes one exact queued Dex Channel message. A repeated delete returns
the SDK's message-not-found error. After an ambiguous network result, every
client reconciles with Snapshot instead of looking up a stored command result.

## Application history and Snapshot

Application history is `CurrentMessages`, `ArchivedMessages`, and range metadata
in `AgentState`. It is not Dex execution history. Fixed-width monotonic sequence
keys identify committed messages. Context reconstruction reads known retained
keys and never enumerates an unbounded map. Compaction commits a cumulative
summary and covered sequence before deleting messages.

Dex loads ordinary Attributes automatically, but Steps and RPCs declare bounded
AttributeMap and Channel loads explicitly. `Snapshot` loads every current
message plus pending queued and steered messages in one read-only invocation.
It never consumes a Channel. At twenty current messages, the oldest ten move
atomically to one archive chunk. The archive endpoint reads one exact adjacent
chunk for upward scrolling.

`Client.MessagesAfter` reads at most 200 retained messages in ascending order
after an exclusive sequence cursor. Watermarks let an adapter detect retention
gaps. A concurrent archive move is retried through its immutable chunk; a trim
returns `HistoryMessageNotFoundError`.

Snapshot is the only durable browser read model. It returns the Dex Run ID,
history, interaction description, and pending messages with their Dex Channel
IDs. Terminal Snapshot follows Dex result and visibility contracts and contains
no active Agent description.

## Recovery and failure policy

- Start persists `RuntimeMetadata` as an initial Attribute and uses
  `IDReuseDisallow`. Empty metadata becomes `{}`.
- Runtime metadata is trusted, opaque routing data. It is passed to tools with
  stable Flow and call IDs after Worker replacement, but never to providers,
  browser state, or Streams.
- The browser reads Snapshot after every mutation success or failure, at durable
  waiting boundaries, on explicit retry, and on its visible-page freshness
  fallback. Mutation controls remain gated until reconciliation succeeds.
- Model calls have bounded attempts, total duration, method timeout, and a
  heartbeat timeout sized for expected provider silence.
- Read-only MCP tools may retry within an explicit budget. Write or unknown MCP
  tools require approval and default to one attempt.
- Stable Flow and call IDs let a tool integration derive an external-effect
  idempotency key without creating a general Agent command ledger.
- A timeout after an unprotected write records an unknown outcome; it never
  claims success or a known failure.
- Failed durable commits retry without exposing staged Attribute or Channel
  changes.
- BlobCache and Streams may disappear. A replacement Worker reconstructs all
  required state from Dex.
