# ADR 0006: Dual-boundary Snapshot reconciliation

## Status

Accepted on 2026-09-10. Supersedes the reconciliation policy in ADR 0004.

## Context

Reading Snapshot only when the Agent entered a durable wait left the browser's
optimistic queue, Plan, approval, and input state unverified after mutations.
A fixed ten-second interval could also fire immediately after another Snapshot,
adding cost without improving freshness.

## Decision

Snapshot is a mutation validation gate at both sides of user interaction. The
browser reads it after `AgentInteractionStatus=waiting` before enabling mutation
controls. Every mutation success or failure starts another blocking read. The
browser applies optimistic success first, but Send, answer, approval, Plan
execution, steer, edit, and delete stay disabled until reconciliation succeeds.
A failed read leaves the view stale and disabled until Retry succeeds.

The coordinator permits one request at a time and merges concurrent triggers
into at most one trailing request. Each blocking trigger increments an epoch.
A response started before the newest epoch is discarded.

The interaction-status long poll also closes stale operation boundaries.
`submitted` immediately disables Plan actions without reading Snapshot. A later
`waiting` result still requires blocking Snapshot reconciliation before the
browser can expose Execute or Continue for the latest revision.

The visible-page freshness fallback is a single-shot timer. Any Snapshot start
cancels it. Completion schedules the next attempt ten seconds later, including
after failure. A hidden page pauses the timer. On visibility restoration, the
browser waits the remaining delay or reads immediately when overdue. A terminal
Snapshot cancels requests, Streams, Attribute waits, and future timers.

## Consequences

The browser never accepts a second mutation against a view that has not been
durably validated after the previous boundary. An active reconciliation resets
the full fallback delay, avoiding a redundant request one second after a read
that began at the ninth second.
