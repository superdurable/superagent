# ADR 0004: Attribute-driven reconciliation and message archives

## Status

Accepted on 2026-09-09. Supersedes ADR 0003.

## Context

Activity-triggered and command-triggered Snapshot reads duplicated work and
could still miss a terminal Flow after Stream loss. Loading the complete
retained history map also made every recovery read grow with the conversation.

The released Dex Go SDK `v0.4.0` provides `WaitForAttributeMatch` and exact
`GetAttributeMapInstance` reads. Streams remain disposable latency hints.

## Decision

`AgentInteractionStatus` is a durable Attribute with `submitted` and `waiting`
values. Commands and automatic timer continuation write `submitted`. A durable
wait commits `waiting`; the following Execute boundary restores `submitted`.
The browser alternates long polls for the two values and reads Snapshot after
`waiting`. Inactive long polls, server errors, explicit reconcile, and a visible
page's ten-second lifecycle fallback also request Snapshot. Initial load is the
only unconditional Snapshot. A terminal Snapshot stops all polls and Streams.

`CurrentMessages` stores individual messages. At twenty current messages, the
oldest ten move atomically into one `ArchivedMessages` instance keyed by its
first sequence. Snapshot loads only the current map. The archive HTTP endpoint
reads exactly one ten-message instance by `beforeSequence`. Retention deletes
only complete archived chunks already covered by a committed summary.

Activity Stream events contain one bounded single-line summary. Each event read
by the browser becomes an independent transient conversation-timeline row keyed
by its resume token. Activity, reasoning summaries, and live assistant text are
not added to Snapshot or application-history archives. Reload starts with an
empty token and may replay events still available from the Dex retained head.

`plan_task_updated` activity includes its base revision, target revision, task
index, and status. The browser applies the hint only to the matching Plan
revision. A later Snapshot replaces all live Plan hints.

## Consequences

Normal commands and Stream events do not cause Snapshot requests. The fallback
still discovers completed, failed, canceled, or terminated Flows without a
final event. Snapshot cost is bounded by nineteen current messages plus pending
Channels. Older history costs one exact archive read per top-scroll action.

The transient timeline may be incomplete after reload because Stream retention
is deliberately not a durable history mechanism. Distinct resume tokens remain
distinct rows even when their safe summaries match exactly.

The released SDK does not expose Channel size in a `WaitFor` invocation.
Therefore `waiting` is committed at the durable wait boundary and `submitted`
is committed at its Execute boundary; real-server tests protect this contract.
