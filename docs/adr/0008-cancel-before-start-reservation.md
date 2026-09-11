# ADR 0008: Isolate cancel-before-start reservation

## Status

Accepted on 2026-09-10.

## Context

A control plane may cancel an Agent before its delayed start reaches Dex. A
plain stop of a missing Flow leaves a race in which the later start can still
create the Agent. This lifecycle race does not justify command fingerprints,
replay receipts, or acceptance ledgers for every RPC.

## Decision

`Client.Cancel` first attempts to start the normal Agent Flow under the target
Flow ID with only an `AgentTerminalReservation` initial Attribute. Dex
`IDReuseDisallow` orders that reservation against a concurrent normal start.
`Init` detects the reservation and performs no Agent work. The client then uses
the released Dex stop operation. If the caller disappears first, the reserved
Flow remains inert until a later cancellation finishes it, and its ID cannot be
reused.

If normal start wins, cancellation stops that active Flow. A repeated call
against any terminal outcome returns `AgentAlreadyTerminalError` with the
actual status; it is not reported as a successful replay. A later normal start
cannot reuse a reserved Flow ID.

## Consequences

The cancel-before-start race is closed by one lifecycle-specific Attribute.
Normal start and command RPCs remain independent of the reservation and do not
gain a general idempotency system. A caller that receives an ambiguous result
reads Snapshot to determine the Flow's current status.
