# ADR 0005: Mutation revisions and terminal reservations

## Status

Accepted on 2026-09-10.

## Context

An embedding control plane may retry commands after ambiguous transport
failures and may issue cleanup before an Agent start reaches Dex. Independent
request IDs prevent duplicate effects, but they do not provide optimistic
coordination across different commands. Stopping a missing Flow also leaves a
race in which a delayed start can create it afterward.

## Decision

Each Agent owns an `AgentMutationRevision` Attribute. Every external mutation
RPC locks it. A newly accepted effect increments it; exact command and
domain-effect replays return the original revision without incrementing it.
Send and Steer accept an optional expected revision and record a typed stale
outcome after replay lookup. Transactional Snapshot returns the same revision
without acquiring the mutation lock.

Send loads queued and steered Channels together. It admits a new message only
when their combined pending count is below 200 and the resulting aggregate
content is at most 256 KiB. One message is also bounded at 256 KiB before
invocation.

Cancellation uses a typed request whose fingerprint includes its reason.
`EnsureCanceled` tries to start the normal Agent Flow under the target Flow ID
with an initial cancellation command, revision, and terminal reservation. Dex
disallows reuse of that Flow ID. `Init` performs no work when the reservation
exists, and the client drives the reserved Flow to canceled. If an Agent won
the start race, cancellation uses its normal transactional RPC before stopping
it.

Trusted embedding applications can verify recovery ownership with
`Client.VerifyIdentity`. It compares a caller-provided canonical application
context with the immutable persisted value and returns only the Flow identity
and whether cancellation reserved it before start. It does not add trusted
routing context to the browser Snapshot.

## Consequences

Embedding applications can fence stale browser or control-plane writes without
losing exact replay. Concurrent sends cannot exceed either combined pending
limit.
A cleanup request can permanently reserve an Agent identity before provisioning
reaches the start call. The reserved Flow may remain active after a caller
crash, but it cannot execute Agent work, and either a later start or cancellation
finishes its terminal transition.
