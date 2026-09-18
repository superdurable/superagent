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
  `ResolveToolRecovery`, `ExecutePlan`, `DeleteQueuedMessage`, `GetSnapshot`, and
  `GetArchivedMessages`
- Browser synchronization Attribute: `WaitingInputRound`

The implementation requires Dex Go SDK and Server `v0.9.0`. Each
`WaitFor`, `Execute`, and RPC invocation is an independent Dex atomic commit.
Provider and MCP calls are external effects and are not part of a Dex
transaction.

New Agent Flows set `FlowConfig.StepDurability` to ASYNC. Ordinary Steps inherit
that default. `CompactContext`, `CallModel`, and tools declared long-running
override Execute durability to SYNC. A short-running tool may fall back from
local to regular execution; that is an expected optimization path and does not
change its ASYNC durability. Registry policy supplies each tool's attempt,
heartbeat, retry, and recovery settings. Ordinary Step methods use a one-minute
timeout, while model methods retain their explicit ten-minute timeout and
five-minute heartbeat.

The `v0.9.0` Worker negotiates the highest common protocol with the Server before
Attribute index synchronization or Worker binding. Deploy the Server before the
Worker. Startup fails when `GetServerInfo` is missing, either interval is
invalid, or the intervals do not overlap.

Renewable sandbox credentials are not an Agent Flow resource. A future,
separately designed `SandboxLifecycleFlow` will own that lifecycle.

## Step graph

```text
Init -> AwaitUser
         -> CheckSteered -> CompactContext? -> CallModel
         -> accepted plan execution ------------^
         -> AnsweredInput -> CompactContext? -> CallModel
              ^
AnswerQuestions RPC -> AnsweredUserInputs Channel

CallModel
  -> CheckSteered -> CallModel                 (first active-plan no-progress response)
  -> CheckSteered -> AwaitUser                 (ordinary or repeated no-progress response)
  -> CheckSteered -> RouteTool                 (tool calls)

RouteTool
  -> CheckSteered -> AwaitToolApproval         (untrusted write)
  -> CheckSteered -> ExecuteToolWithRetry      (approved/read-only external tool)
  -> ExecuteParallelTool + AwaitParallelToolResults (bounded contiguous safe reads)
  -> CheckSteered -> DurableWait               (timer tool)
  -> AwaitUser                                  (durable input tool)
  -> next tool or CompactContext               (built-in/result)

AwaitToolApproval
  -> CheckSteered -> ExecuteToolWithRetry      (approved)
  -> next tool or CompactContext               (rejected)
  -> CompactContext                            (steered)

ExecuteToolWithRetry
  -> next tool or CompactContext               (success or known failure)
  -> RecoverToolExecution                      (configured automatic unknown)
  -> PrepareManualToolRecovery                 (default retry exhaustion)

RecoverToolExecution
  -> next tool or CompactContext               (one unknown outcome)

ExecuteParallelTool
  -> AwaitParallelToolResults ChannelMap       (one typed branch result)

AwaitParallelToolResults
  -> next tool or CompactContext               (all results final)
  -> AwaitManualToolRecovery                   (one or more manual unknowns)

PrepareManualToolRecovery
  -> AwaitManualToolRecovery                   (capture redacted error type)

AwaitManualToolRecovery
  -> retry failed calls                        (complete per-call decisions)
  -> next tool or CompactContext               (continue unknown decisions)
  -> AwaitUser                                 (stop)
  -> CompactContext                            (steered replan)

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
| `AwaitUser`            | answer only while a question is pending; otherwise steering, one queued message, or current plan execution | Persist waiting status; increment the round only at a real input boundary; consume the selected answer, steering, queued, or Plan input |
| `AnsweredInput`        | none                                                                                | Route an accepted answer through compaction directly to the model before checking steering                                                                 |
| `CompactContext`       | none                                                                                | Call the summary provider, commit the covered range and summary, then trim only summarized retained messages                                               |
| `CallModel`            | none                                                                                | Rebuild context, stream buffered deltas, commit the assistant message and pending calls; retry one active-plan response that made no durable progress      |
| `CheckSteered`         | bounded steered batch                                                               | Apply steering at a safe boundary or route the explicit continuation                                                                                       |
| `RouteTool`            | none                                                                                | Validate built-in arguments and select approval, MCP execution, timer, input, or next-call path                                                            |
| `AwaitToolApproval`    | exact call-ID approval or steering                                                  | Persist waiting status; consume one decision or replan on steering                                                                                         |
| `ExecuteToolWithRetry` | none                                                                                | Perform one external tool attempt under dynamically selected Dex timeout and retry policy                                                                  |
| `RecoverToolExecution` | none                                                                                | Record one unknown result for an explicitly configured automatic recovery, then continue                                                                   |
| `ExecuteParallelTool`  | none                                                                                | Perform one bounded-wave branch effect and publish exactly one typed result without shared-state mutation                                                   |
| `RecoverParallelToolExecution` | none                                                                        | Convert one exhausted branch to an unknown typed result and publish it                                                                                      |
| `AwaitParallelToolResults` | all started branch results                                                                 | Join results, preserve model order, and either commit the batch or enter one manual recovery                                                                |
| `PrepareManualToolRecovery` | none                                                                            | Capture the serial exhausted call and redacted Dex error type                                                                                               |
| `AwaitManualToolRecovery` | exact recovery decision or steering                                               | Persist recovery state; retry selected calls, continue unknowns, stop the sequence, or replan                                                               |
| `DurableWait`          | Timer or steering                                                                   | Persist waiting status; record completion or interruption and continue                                                                                     |

Dex Server and Go SDK `v0.9.0` expose Channel size metadata in `WaitFor` and
`Execute`. `AwaitUser.WaitFor` reads the
sizes of `SteeredUserMessages`, `QueuedUserMessages`, and the current
`PlanExecutions` instance without loading message payloads. It increments
`WaitingInputRound` only when the selected Channel set is empty and the Step
will actually suspend. It checks `AnsweredUserInputs` before
`PendingUserInput`, so an answer published before WaitFor is consumed
immediately. While a question remains pending, it waits only on that answer
Channel; other messages remain queued for later turns.
Approval and timer waits never change the round. The
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
| `PendingToolRecovery`  | Attribute    | Exact recovery revision and redacted failed-call presentation                                |
| `PendingTimer`         | Attribute    | Current durable wait presentation                                                            |
| `PendingUserInput`     | Attribute    | Current structured question batch                                                            |
| `AnsweredUserInputs`   | Channel      | Validated answers awaiting immediate consumption                                             |
| `QueuedUserMessages`   | Channel      | FIFO `PendingUserMessage` payloads with stable application IDs                               |
| `SteeredUserMessages`  | Channel      | Safe-boundary steering payloads preserving the same application IDs                          |
| `ToolApprovals`        | ChannelMap   | Approval decisions keyed by tool call ID                                                     |
| `ToolRecoveryDecisions` | ChannelMap  | Atomic complete recovery decisions keyed by recovery ID                                      |
| `ParallelToolResults`  | ChannelMap   | One typed result from each bounded parallel execution branch                                  |
| `PlanExecutions`       | ChannelMap   | Execution request keyed by Plan revision                                                     |
| `ReasoningSummary`     | Stream       | Buffered best-effort provider summary deltas                                                 |
| `AssistantText`        | Stream       | Buffered best-effort assistant deltas                                                        |
| `AgentActivity`        | Stream       | Structured best-effort activity, input-consumption hints, and hidden Snapshot controls       |

Channels deliver work and never replace durable application history. A queued
message enters `CurrentMessages` only when an Execute consumes it.

## Commands and stable message identity

The Agent Client generates one application `MessageID` before invoking Send.
The same ID survives Dex RPC retries and remains in the payload when a message
moves from the queued Channel to the steered Channel. Dex Channel envelope IDs
remain private implementation details.

Snapshot exposes application message IDs. `DeleteQueuedMessage` and
`SteerMessage` accept only IDs present in Snapshot, scan loaded pending payloads
for the match, then delete the corresponding Dex envelope. A stale or repeated
ID is rejected without changing either Channel.

`AnswerQuestions` validates the exact pending call and complete question set,
deletes the pending batch, and publishes one typed `AnsweredUserInput`
atomically. `AwaitUser.Execute` consumes it, appends the answer to
`CurrentMessages`, emits `user_input_answered`, and enters `AnsweredInput`.
That Step reaches the model before steering or queued input can continue.
`ExecutePlan` accepts only the current revision at an executable durable wait.

## Input-consumption activity

Every Execute that consumes a queued or steered message emits one
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

Answer consumption emits `user_input_answered` after the answer enters
`CurrentMessages`. The event carries the answered call ID and durable message
sequence, but not the answer text. The browser correlates it with the locally
submitted answer for immediate display and requests Snapshot for authoritative
reconciliation.

The approval, manual recovery, and Timer target `WaitFor` methods write a `snapshot_required`
control event after `RouteTool` commits their durable payload. The browser hides
this event and requests one non-blocking Snapshot. These waits do not advance
`WaitingInputRound`.

## Snapshot and history

`CurrentMessages` and `ArchivedMessages` are application history, not Dex
execution history. Explicit monotonic sequence metadata defines ordering and
archive boundaries. AttributeMap instances use unpadded decimal sequence keys.
Context compaction commits a summary and its covered range before deleting
retained messages.

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
- `short_running` is the default and inherits Flow ASYNC durability. Use
  `long_running` when more than half of expected calls are likely to exceed five
  seconds; it overrides Execute durability to SYNC. This classification is an
  optimization hint, not a runtime guarantee.
- Tool heartbeat defaults to one minute. Increase it only when healthy regular
  execution can remain silent for longer. `AttemptTimeout` also bounds the
  registry context because ASYNC local execution ignores Dex method timeouts.
- The `mock/dex` model alone exposes `simulate_tool_failure`; `/tool-failure`
  uses it to verify retry exhaustion and the manual recovery surface locally.
- Known business failures return a normal tool result. Transient or ambiguous
  errors use Dex retry. A returned unknown does not retry. Unknown outcomes and
  retry exhaustion wait for manual recovery unless trusted per-tool policy
  explicitly selects automatic continuation.
- Safe read-only calls form contiguous bounded waves. Branches can finish in any
  order, but the join commits all final messages in original model order.
- Manual resume decisions must cover every currently failed call. Retry starts a
  fresh full Dex retry policy with the same call ID. Stop and steering do not
  invoke the model after the interrupted sequence.
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
