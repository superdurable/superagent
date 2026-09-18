# ADR 0012: Reconcile with a waiting watermark and input-consumption events

## Status

Accepted on 2026-09-14. Supersedes the synchronization policy in ADRs 0004,
0006, and 0007. Supersedes ADR 0010's placement of credential maintenance.

## Context

Alternating `AgentInteractionStatus` between `submitted` and `waiting` made the
browser issue equality waits. Re-establishing the same logical wait introduced
additional Temporal synchronization updates and made frequent Snapshot reads
expensive. The status also mixed two concerns: discovering a new durable input
boundary and removing inputs that an Execute had already consumed.

Dex Server `v0.7.0` supplies Channel and ChannelMap size metadata to the Worker
and supports reconnecting one logical Attribute match through explicit
options. Dex Go SDK `v0.7.0` mistakenly restricted the public size accessor to
RPC handlers. SDK `v0.7.1` exposes it in `WaitFor` and `Execute`.

The Coding Agent Flow also maintained renewable sandbox credentials. That
lifecycle has a different identity and operational lifetime from a conversation.

## Decision

`WaitingInputRound` is an `int64` Attribute initialized to zero and bounded by
JavaScript's safe integer maximum. `AwaitUser.WaitFor` reads Channel size
metadata for steered messages, queued messages, and the current Plan execution
instance without loading payloads. It increments the round only when all
relevant inputs are empty and the Step will truly suspend. While a question is
pending, the Step increments the round only when the answer Channel is empty,
then waits exclusively for that Channel. An answer already published before
WaitFor completes immediately. Unrelated queued input does not create a false
completion. Other waits never change the round.

The browser reads the initial round from Snapshot, waits for a strictly greater
value, adopts the actual matched value as its next watermark, and requests a
new Snapshot. It does not create an equality lifecycle probe after Snapshot.
The Snapshot backend checks indexed Flow lifecycle before invoking its durable
read RPC so a terminal Flow cannot return the last running projection. The
visible-page fallback is configurable at runtime and defaults to 60 seconds.

Send payloads receive one stable application message ID before RPC retry.
Steering preserves it. Snapshot exposes the application ID while Dex
Channel envelope IDs remain private.

An Execute that consumes queued or steered messages, or a Plan execution
request, writes an `input_consumed` Activity event. The structured payload
contains exact message IDs and nullable Plan revision, while display text
contains only counts or revision. The frontend applies it idempotently and
closes Plan actions until Snapshot confirms the next real waiting boundary.
The approval and Timer target `WaitFor` methods write a hidden
`snapshot_required` Activity control after `RouteTool` commits their durable
payload. The browser uses it for one non-blocking Snapshot because those waits
intentionally do not advance the watermark.

`AnswerQuestions` durably publishes a validated answer to its dedicated
Channel. `AwaitUser.Execute` appends it to application history and writes a
`user_input_answered` Activity carrying the call ID and durable message
sequence. The browser displays its locally known answer immediately and uses
Snapshot to reconcile authoritative history.

Runtime Lease Attributes, Steps, public options, and tool injection are removed
from `AIAgentFlow`. Renewable sandbox credentials will belong to a separately
designed `SandboxLifecycleFlow`; this change creates no placeholder contract.

## Consequences

One long poll advances monotonically instead of alternating equality waits.
Inputs already waiting at `AwaitUser` do not create false browser boundaries.
Queue removal is immediate when the Activity Stream is available, while
payloads already known from Snapshot remain visible as temporary user bubbles.
Consumed IDs suppress stale queue data until durable history replaces the
temporary projection. Snapshot remains the only authoritative durable
reconciliation model.

Deployments must use Dex Server `v0.10.0` and Go SDK `v0.9.1`, and the matching
Worker and browser behavior together. The Server must be upgraded first because
Workers reject Servers without protocol negotiation. Deployments must stop or
clear Agent Flows created with the removed schema before rollout; there is no
old-Attribute or Runtime Lease compatibility shim.
