# ADR 0016: Let the Agent own inactivity expiration

## Status

Accepted on 2026-09-23.

## Context

Session infrastructure cannot infer user inactivity from API traffic. Snapshot
and Stream reads are observation, while a `durable_wait` may intentionally
suspend longer than an inactivity window. Copying
deadlines into multiple lifecycle Flows also requires publishing every activity
to every resource owner and creates competing shutdown decisions.

## Decision

`AIAgentFlow` owns the inactivity decision. A positive
`inactivity_timeout_seconds` starts one parallel `InactivityTimeout` Step and
persists `InactivityDeadline`. Accepted user RPCs advance the Attribute and
publish `ResetInactivityTimer` only when the extension exceeds one minute.
Model and ordinary tool work do not pause the deadline. `DurableWait` advances
it to the wait target plus the configured timeout.

Expiration invokes the constructor-injected `InactivityExpirationHandler`.
The handler receives Flow ID, Run ID, deadline, and trusted runtime metadata.
Those values form a stable idempotency identity across retries and Worker
replacement. Dex retries the callback for a bounded seven-day window, and the
Agent commits `expiring`, emits `inactivity_expired`, and force-completes only
after the callback succeeds.

## Consequences

Embedders configure the timeout and own cross-resource cleanup behind the
handler. They do not report each activity back to lifecycle infrastructure.
Snapshots expose the exact deadline for the configured Agent lifecycle. A
failed callback retains the expired deadline until retry succeeds or the
explicit retry policy is exhausted.
