# ADR 0004: Attribute-driven reconciliation and message archives

## Status

Accepted on 2026-09-09. Supersedes ADR 0003.

## Context

Activity-triggered and command-triggered Snapshot reads duplicated work and
could still miss a terminal Flow after Stream loss. Loading the complete
retained history map also made every recovery read grow with the conversation.

The released Dex Go SDK `v0.2.12` provides `WaitForAttributeEqual` and exact
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

Activity Stream events contain one bounded single-line summary. The UI replaces
one activity row instead of accumulating an event log.

## Consequences

Normal commands and Stream events do not cause Snapshot requests. The fallback
still discovers completed, failed, canceled, or terminated Flows without a
final event. Snapshot cost is bounded by nineteen current messages plus pending
Channels. Older history costs one exact archive read per top-scroll action.

The released SDK does not expose Channel size in a `WaitFor` invocation.
Therefore `waiting` is committed at the durable wait boundary and `submitted`
is committed at its Execute boundary; real-server tests protect this contract.
