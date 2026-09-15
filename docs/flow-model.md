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
  `ExecutePlan`, `DeleteQueuedMessage`, `GetSnapshot`, and
  `GetArchivedMessages`
- Browser synchronization Attribute: `WaitingInputRound`

The implementation requires Dex Go SDK `v0.7.1` and Server `v0.7.0`. Each
`WaitFor`, `Execute`, and RPC invocation is an independent Dex atomic commit.
Provider and MCP calls are external effects and are not part of a Dex
transaction.

Renewable sandbox credentials are not an Agent Flow resource. A future,
separately designed `SandboxLifecycleFlow` will own that lifecycle.

## Step graph

```text
Init -> AwaitUser
         -> CheckSteered -> CompactContext? -> CallModel
         -> accepted plan execution ------------^

CallModel
  -> CheckSteered -> CallModel                 (first active-plan no-progress response)
  -> CheckSteered -> AwaitUser                 (ordinary or repeated no-progress response)
  -> CheckSteered -> RouteTool                 (tool calls)

RouteTool
  -> CheckSteered -> AwaitToolApproval         (untrusted write)
  -> CheckSteered -> ExecuteToolWithRetry      (approved/read-only external tool)
  -> CheckSteered -> DurableWait               (timer tool)
  -> AwaitUser                                  (durable input tool)
  -> next tool or CompactContext               (built-in/result)

AwaitToolApproval
  -> CheckSteered -> ExecuteToolWithRetry      (approved)
  -> next tool or CompactContext               (rejected)
  -> CompactContext                            (steered)

ExecuteTool
  -> next tool or CompactContext               (legacy open executions only)

ExecuteToolWithRetry
  -> next tool or CompactContext               (success or known failure)
  -> RecoverToolExecution                      (Dex retry exhaustion)

RecoverToolExecution
  -> next tool or CompactContext               (one unknown outcome)

DurableWait
  -> next tool or CompactContext               (timer fired)
  -> CompactContext                            (steered)
```

`CheckSteered` is the safe-boundary router. It never cancels an in-flight model
or MCP call. A steered message clears stale approval, timer, and pending input
state, persists cancellation results for abandoned calls, enters application
history, and makes the model replan.

## Step responsibilities

| Step                   | `WaitFor`                                                                           | `Execute` and transition                                                                                                                                   |
| ---------------------- | ----------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Init`                 | none                                                                                | Validate and persist config/state, initialize round zero, then enter `AwaitUser`                                                                           |
| `AwaitUser`            | steering, one queued message, or current plan execution when no question is pending | Persist waiting status; increment the round only when all relevant Channel sizes are zero; prioritize steering and consume one selected input |
| `CompactContext`       | none                                                                                | Call the summary provider, commit the covered range and summary, then trim only summarized retained messages                                               |
| `CallModel`            | none                                                                                | Rebuild context, stream buffered deltas, commit the assistant message and pending calls; retry one active-plan response that made no durable progress      |
| `CheckSteered`         | bounded steered batch                                                               | Apply steering at a safe boundary or route the explicit continuation                                                                                       |
| `RouteTool`            | none                                                                                | Validate built-in arguments and select approval, MCP execution, timer, input, or next-call path                                                            |
| `AwaitToolApproval`    | exact call-ID approval or steering                                                  | Persist waiting status; consume one decision or replan on steering                                                                                         |
| `ExecuteTool`          | none                                                                                | Perform one external MCP effect with stable Flow/call identity, then persist its result                                                                    |
| `ExecuteToolWithRetry` | none                                                                                | Perform one external tool attempt under dynamically selected Dex timeout and retry policy                                                                  |
| `RecoverToolExecution` | none                                                                                | Record one unknown result after exhausted Dex retries, then let the Agent continue                                                                         |
| `DurableWait`          | Timer or steering                                                                   | Persist waiting status; record completion or interruption and continue                                                                                     |

Dex Server `v0.7.0` supplies Channel size metadata to the Worker, and Dex Go SDK
`v0.7.1` exposes it in `WaitFor` and `Execute`. `AwaitUser.WaitFor` reads the
sizes of `SteeredUserMessages`, `QueuedUserMessages`, and the current
`PlanExecutions` instance without loading message payloads. It increments
`WaitingInputRound` only when the selected Channel set is empty and the Step
will actually suspend. Approval and timer waits never change the round. The
next value must remain within JavaScript's safe integer range or the WaitFor
fails explicitly.

## Durable resources

| Resource               | Kind         | Purpose                                                                                      |
| ---------------------- | ------------ | -------------------------------------------------------------------------------------------- |
| `AgentConfig`          | Attribute    | Immutable execution configuration                                                            |
| `AgentRuntimeMetadata` | Attribute    | Trusted runtime routing metadata; never model or browser context                             |
| `AgentState`           | Attribute    | Sequence range, mode, status, plan revision, pending-call cursor, and Plan no-progress count |
| `WaitingInputRound`    | Attribute    | Monotonic durable browser watermark, initialized to zero                                     |
| `ContextSummary`       | Attribute    | Cumulative summary and explicit covered sequence                                             |
| `CurrentMessages`      | AttributeMap | Recent provider-neutral messages keyed by sequence                                           |
| `ArchivedMessages`     | AttributeMap | Ten-message chunks keyed by first sequence                                                   |
| `AgentPlan`            | Attribute    | Atomically replaced short plan with revision and task status                                 |
| `PendingApproval`      | Attribute    | Current exact approval boundary                                                              |
| `PendingTimer`         | Attribute    | Current durable wait presentation                                                            |
| `PendingUserInput`     | Attribute    | Current structured question batch                                                            |
| `QueuedUserMessages`   | Channel      | FIFO `PendingUserMessage` payloads with stable application IDs                               |
| `SteeredUserMessages`  | Channel      | Safe-boundary steering payloads preserving the same application IDs                          |
| `ToolApprovals`        | ChannelMap   | Approval decisions keyed by tool call ID                                                     |
| `PlanExecutions`       | ChannelMap   | Execution request keyed by Plan revision                                                     |
| `ReasoningSummary`     | Stream       | Buffered best-effort provider summary deltas                                                 |
| `AssistantText`        | Stream       | Buffered best-effort assistant deltas                                                        |
| `AgentActivity`        | Stream       | Structured best-effort activity, input-consumption hints, and hidden Snapshot controls       |

Channels deliver work and never replace durable application history. A queued
message enters `CurrentMessages` only when an Execute consumes it.

## Commands and stable message identity

The Agent Client generates one application `MessageID` before invoking Send or
Answer. The same ID survives Dex RPC retries and remains in the payload when a
message moves from the queued Channel to the steered Channel. Dex Channel
envelope IDs remain private implementation details.

Snapshot exposes application message IDs. `DeleteQueuedMessage` and
`SteerMessage` accept only IDs present in Snapshot, scan loaded pending payloads
for the match, then delete the corresponding Dex envelope. A stale or repeated
ID is rejected without changing either Channel.

`AnswerQuestions` validates the exact pending call and complete question set,
deletes the pending batch, and publishes one answer payload atomically.
`ExecutePlan` accepts only the current revision at an executable durable wait.

## Input-consumption activity

Every Execute that consumes queued or steered messages emits one
`input_consumed` event in the same Dex commit. `AwaitUser` also emits the event
for a consumed Plan request, including a request that became stale in a race.
The payload contains only `queuedMessageIds`, `steeredMessageIds`, and nullable
`planExecutionRevision`. The display message contains counts or the revision,
never user content.

The browser removes only matching IDs and clears only the matching Plan
revision. It temporarily projects consumed payloads already present in Snapshot
as user messages, while consumed IDs hide any stale queue Snapshot. Replayed
events are therefore idempotent and cannot delete newer inputs. Durable history
replaces the temporary projection on the next current Snapshot. The event also
closes the transient Plan-action gate until a later Snapshot confirms a real
waiting boundary. Snapshot remains the only durable current-interaction read
model and corrects missed Stream events.

Approval and Timer setup write a `snapshot_required` control event after their
durable payload is committed. The browser hides this event and requests one
non-blocking Snapshot. These waits do not advance `WaitingInputRound`.

## Snapshot and history

`CurrentMessages` and `ArchivedMessages` are application history, not Dex
execution history. Fixed-width monotonic sequence metadata defines ordering and
archive boundaries. Context compaction commits a summary and its covered range
before deleting retained messages.

Snapshot is one read-only Flow RPC that loads current history, the interaction
description, and pending Channels. It returns `WaitingInputRound` and stable
application message IDs. Archive paging loads exactly one immutable chunk and
the bounded sequence metadata needed for continuation.

The browser begins with the Snapshot round, waits for `round > watermark`, uses
the actual matched round as the next watermark, then refreshes Snapshot. A
configurable visible-page fallback defaults to 60 seconds and resets after
every completed Snapshot read. Hidden pages pause the timer and live reads.

## External effects and recovery

- Tool execution policy is copied from `ToolDefinition` into Dex StepOptions.
- Known business failures return a normal tool result. Transient or ambiguous
  errors use Dex retry. Exhaustion records one unknown result and continues.
- Stable Flow and call IDs let tool integrations derive external-effect
  idempotency keys without creating a general Agent command ledger.
- A timeout after an unprotected write records an unknown outcome; it never
  claims success or a known failure.
- Failed durable commits retry without exposing staged Attribute or Channel
  changes.
- BlobCache and Streams may disappear. A replacement Worker reconstructs all
  required state from Dex.
- Stream recovery lists only the configured recent tail. Snapshot repairs any
  durable projection missed because an event expired or a connection failed.
