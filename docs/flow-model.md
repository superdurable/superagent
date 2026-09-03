# AI Agent Flow model

## Identity and lifecycle

- Flow type: `AIAgentFlow`
- Business identity: one stable Flow ID per durable Agent conversation
- Start input: typed `AgentConfig`
- Completion: intentionally open-ended in Phase 1; the Agent waits for the next
  user command after each turn
- Command RPCs: `SendMessage`, `SteerMessage`, `ApproveTool`, and `ExecutePlan`
- Read RPC: `Snapshot`

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

| Step | `WaitFor` | `Execute` and transition |
|---|---|---|
| `Init` | none | Validate and persist config/state, then `AwaitUser` |
| `AwaitUser` | steered batch, one queued message, or current plan execution | Persist waiting status beside the wait; consume exactly one normal message or the selected durable command |
| `CompactContext` | none | Call the summary provider, commit covered range and summary, then trim only already summarized retained messages |
| `CallModel` | none | Rebuild provider-neutral context, stream buffered deltas, commit the complete assistant message and pending calls |
| `CheckSteered` | bounded steered batch | Apply steering at a safe boundary or route the explicit continuation |
| `RouteTool` | none | Validate built-in arguments and select approval, MCP execution, timer, input, or next-call path |
| `AwaitToolApproval` | exact call-ID approval or steering | Persist waiting status beside the wait; consume one decision or replan |
| `ExecuteTool` | none | Perform one external MCP effect with stable call ID and bounded policy, then persist its result |
| `DurableWait` | Timer or steering | Persist waiting status beside the wait; record completion/interruption and continue |

## Durable resources

| Resource | Kind | Purpose |
|---|---|---|
| `AgentConfig` | Attribute | Immutable execution configuration |
| `AgentState` | Attribute | Sequence range, mode, status, plan revision, and pending-call cursor |
| `ContextSummary` | Attribute | Cumulative summary and explicit covered sequence |
| `AgentMessages` | AttributeMap | Provider-neutral application history keyed by monotonic sequence |
| `AgentPlan` | Attribute | Atomically replaced short plan |
| `PendingApproval` | Attribute | Reloadable approval request |
| `PendingTimer` | Attribute | Reloadable durable wait description |
| `PendingUserInput` | Attribute | Reloadable user question and choices |
| `QueuedUserMessages` | Channel | FIFO messages that do not interrupt active work |
| `SteeredUserMessages` | Channel | Messages consumed only at safe boundaries |
| `ToolApprovals` | ChannelMap | Approval decision partitioned by call ID |
| `PlanExecutions` | ChannelMap | Execution request partitioned by plan revision |
| `ReasoningSummary` | buffered Stream | Provider-authored reasoning summaries only |
| `AssistantText` | buffered Stream | Visible response deltas |
| `AgentActivity` | Stream | Complete lifecycle and tool activity events |

Channels are delivery mechanisms, not storage. A queued message enters
`AgentMessages` only after a Step consumes it. Stream loss never changes durable
truth.

## Application history

Application history is the `AgentMessages` AttributeMap plus range metadata in
`AgentState`. It is not Dex execution history.

Sequence keys are fixed-width monotonic values. Context reconstruction reads
known keys from the retained range and does not enumerate an unbounded map.
Compaction commits a cumulative summary with its exact covered sequence before
deleting messages.

`Snapshot` explicitly loads all `AgentMessages` entries and the pending values
of `QueuedUserMessages` and `SteeredUserMessages`. Ordinary Attributes used for
the description are available under the released RPC semantics. Loading is
independent from locking and transactional execution: this RPC is read-only,
does not lock the resources, and does not consume either Channel. Its history
page is projected only from `AgentMessages` and `AgentState` retention metadata.

The RPC returns the invocation Run ID from Dex context. Consecutive reads retain
Channel FIFO order, values, and stable message IDs. Closed Flows follow the SDK's
active-Flow RPC error contract; the HTTP mapper does not simulate a closed-state
Snapshot.

## Retry and failure policy

- Model calls have bounded attempts, total duration, method timeout, and a
  heartbeat timeout sized for expected provider silence.
- Read-only MCP tools may retry within an explicit budget.
- Write or unknown MCP tools require approval and default to one attempt.
- Stable call IDs are passed through the application boundary for integrations
  that support idempotency.
- A timeout after an unprotected write records an unknown outcome; it never
  claims success or a known failure.
- Failed durable commits retry without exposing staged Attribute or Channel
  changes.
- BlobCache and Streams may disappear. A replacement Worker reconstructs all
  required state from Dex.
