# ADR 0016: Let the Agent own inactivity expiration

## Status

Accepted on 2026-09-23.

## Context

Session infrastructure cannot infer inactivity from API traffic. Snapshot and
Stream reads are observation, model and tool calls are active work, and a
`durable_wait` intentionally suspends longer than an inactivity window. Copying
deadlines into multiple lifecycle Flows also requires publishing every activity
to every resource owner and creates competing shutdown decisions.

## Decision

`AIAgentFlow` owns the inactivity decision. A positive
`inactivity_timeout_seconds` arms a Dex Timer only in `AwaitUser`,
`AwaitToolApproval`, and `AwaitManualToolRecovery`. The same atomic wait writes
`InactivityDeadline`; every active-work or durable-wait path leaves it absent.
Accepted Channel input takes precedence if it becomes ready with the Timer.

Expiration commits the `expiring` status and one `inactivity_expired` activity
event before invoking the constructor-injected `InactivityExpirationHandler`.
The handler receives Flow ID, Run ID, deadline, and trusted runtime metadata.
Those values form a stable idempotency identity across retries and Worker
replacement. Dex retries the callback for a bounded seven-day window, and the
Agent completes only after the callback succeeds.

## Consequences

Embedders configure the timeout and own cross-resource cleanup behind the
handler. They do not report each activity back to lifecycle infrastructure.
Snapshots expose the exact deadline only while inactivity is armed, allowing a
UI to distinguish an active countdown from paused model, tool, and durable-wait
work. A failed callback leaves the Agent durably `expiring` until retry succeeds
or the explicit retry policy is exhausted.
