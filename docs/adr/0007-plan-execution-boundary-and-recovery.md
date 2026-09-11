# ADR 0007: Gate and recover Plan execution

## Status

Accepted on 2026-09-10.

## Context

The browser previously enabled Execute and Continue from Plan status alone.
The server correctly rejected requests while the Agent was running or the
revision was stale, so the visible action could produce an unexplained conflict.
An active Plan could also stop with unfinished tasks when a model response made
no tool call.

## Decision

`ExecutePlan` accepts only the latest draft or active revision while the Agent
is at `waiting_for_message`. Pending input, approval, timer, queued messages,
steering, or a prior execution request closes this boundary. A rejection commits
neither state nor a `PlanExecutions` message.

The browser combines the latest Snapshot with `AgentInteractionStatus`.
`submitted` immediately disables the Plan action without requesting Snapshot.
The next durable `waiting` boundary triggers blocking Snapshot reconciliation;
only that result may expose Execute or Continue. The UI identifies preparation,
running, request, and blocker states instead of presenting a clickable stale
revision.

`AgentState` counts consecutive no-progress responses while an active Plan is
executing. The first response with no tool call and unfinished tasks is
preserved, increments the count, and returns through `CheckSteered` for one
corrective model turn. A second response enters `AwaitUser`. Tool calls, Plan
updates, user input, steering, and explicit Execute or Continue reset the count.

## Consequences

Busy execution is not queued and does not interrupt active work. The correction
limit prevents an infinite model loop while giving the Agent one opportunity to
continue, request structured input, or record accurate Plan progress. Existing
Flows decode the additive counter as zero.
