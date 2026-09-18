# ADR 0015: Keep Snapshot application-only

## Status

Accepted on 2026-09-17.

## Context

Snapshot is the durable current-interaction read model. It previously queried
Dex visibility before its Flow RPC, synthesized a separate terminal response
through `WaitForFlow`, and exposed lifecycle and failure metadata to the browser.
That split the read across independent consistency boundaries and put an
eventually consistent visibility query on every Snapshot request.

Dex Server `v0.10.0` executes an RPC without locks or transactional mode through
Temporal Query. A real Temporal integration test confirms that this path remains
readable after Flow closure. The terminal rejection test in Dex forces all RPCs
through synchronous Update and does not describe the Query path.

## Decision

`GetSnapshot` invokes its registered read-only RPC exactly once. It does not call
`SearchFlows`, `WaitForFlow`, or retry `FlowNotActiveError` or
`LongPollTimeoutError`. Snapshot contains Run ID and durable Agent application
state. It does not contain Dex lifecycle status, terminal failure metadata, or a
nullable terminal description.

Run ID remains the execution-generation key. The browser uses a run change to
discard transient reconciliation state and reset Stream resume tokens across
continue-as-new. The browser has no terminal Snapshot state or terminal result
screen.

## Consequences

Snapshot has one coherent application-state boundary and no visibility-index
dependency. A retained closed Flow returns its last durable Agent view. Missing,
expired, deleted, or globally forced-Update executions can still return their
typed Dex error. Lifecycle inspection is an operational concern outside the
Snapshot HTTP contract.
