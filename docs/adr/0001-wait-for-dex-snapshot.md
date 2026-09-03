# ADR 0001: Wait for the released Dex Snapshot API

- Status: Accepted
- Date: 2026-09-02

## Context

Dex is refactoring Agent history, queue, description, and status reads into one
Snapshot RPC. The design screenshot is directional material, not an available
server or Go SDK contract. Building against it would create speculative code and
multiple state views with different read times.

## Decision

Phase 1 implements durable resources and command behavior but exposes none of
the four legacy read endpoints, Snapshot, or queue mutation HTTP endpoints.

Phase 2 waits for a merged server implementation, released Go SDK, and
version-matched runnable example. It will expose one Snapshot endpoint and use
events only for low-latency deltas between durable reconciliations.

## Consequences

The Phase 1 main conversation page is intentionally not wired to an incomplete
Go read surface. There are no placeholders, compatibility routes, temporary
aggregation services, local SDK replacements, or exported resource getters.

Once available, Snapshot can provide one atomic durable view, simpler recovery,
and a smaller HTTP and frontend state surface.
