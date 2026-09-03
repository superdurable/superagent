# ADR 0001: Wait for the released Dex Snapshot API

- Status: Accepted
- Date: 2026-09-02
- Fulfilled: 2026-09-03 with Dex Go SDK `v0.2.11`

## Context

Dex is refactoring Agent history, queue, description, and status reads into one
Snapshot RPC. The design screenshot is directional material, not an available
server or Go SDK contract. Building against it would create speculative code and
multiple state views with different read times.

## Decision

Phase 1 implements durable resources and command behavior but exposes none of
the four legacy read endpoints, Snapshot, or queue mutation HTTP endpoints.

Phase 2 waited for a merged server implementation, released Go SDK, and
version-matched compile-contract test. It exposes one Snapshot endpoint and uses
events only for low-latency deltas between durable reconciliations.

## Consequences

The Phase 1 main conversation page is intentionally not wired to an incomplete
Go read surface. There are no placeholders, compatibility routes, temporary
aggregation services, local SDK replacements, or exported resource getters.

Dex PRs 442 through 445 and Go SDK `v0.2.11` satisfied the gates. The adopted
resource selection and reconciliation semantics are recorded in ADR 0003.
