# ADR 0014: Register immutable RPC execution options

## Status

Accepted on 2026-09-17.

## Context

Dex Go SDK `v0.9.1` replaces reflected RPC discovery with explicit `GetRPCs`
definitions. Timeout, locks, transactional execution, and selective collection
loads belong to the registered RPC definition. Callers can impose a shorter
context deadline, but cannot change those options per invocation.

Most Agent RPCs always use the same resources. `GetArchivedMessages` is the
exception: its input selects one `ArchivedMessages` instance. The released SDK
cannot derive an AttributeMap instance load from RPC input.

## Decision

`AIAgentFlow.GetRPCs` explicitly registers every production RPC and its complete
execution policy. Client calls provide only the Flow ID, registered method,
typed input, and output destination. Snapshot uses a five-second registered
timeout. Commands and archive reads use twenty seconds. Existing Attribute
locks and transactional Channel mutations remain attached to their RPCs.

`GetArchivedMessages` registers a whole-map `ArchivedMessages` load and returns
only the requested immutable ten-message chunk. It still excludes current
messages and pending Channels. Retention remains the bound on loaded archive
state. Integration-only RPCs use the same explicit registration contract.

The release lock records Server `v0.10.0` through its compatibility manifest.
Because `sdk-go/v0.9.1` is an SDK-only release without a Server manifest, the
lock records its tag, source commit, and Go module checksums separately.

## Consequences

Worker and Client registries share one visible RPC contract, and invalid loads
or locks fail during registry construction. Call sites cannot accidentally
weaken transactional behavior or select undeclared state.

An archive page now hydrates every retained archive chunk before returning one
page. This is a known cost of the released `v0.9.1` contract, not an SLA change.
A future bounded design requires a new durable storage boundary or a released
SDK facility for input-derived instance selection; it must not emulate mutable
per-call options in application code.
