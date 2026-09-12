# ADR 0010: Maintain an optional Runtime Lease inside the Agent Flow

## Status

Accepted on 2026-09-11.

## Context

External coding tools may need short-lived credentials. Starting an Agent
before those credentials exist creates a first-message race. A process-local
refresh loop also loses its schedule across Worker replacement.

## Decision

An embedding application may inject one `LeaseRefresher`. Start then requires
an immediately usable opaque state and its first refresh time. The Client writes
that state and a typed generation schedule as initial Dex Attributes before
`Init` runs.

`Init` starts normal conversation handling beside a maintenance branch. The
branch waits on a Dex Timer, refreshes through an external Execute, and commits
the replacement state and next schedule atomically. Refresh requests carry a
stable ID derived from Flow ID and target generation. The integration must make
that external operation idempotent and schedule refresh well before expiration.

Lease state is capped at 16 KiB. It is available to tools but excluded from
models, HTTP, Snapshot, application history, Streams, activity, and logs.

## Consequences

The first tool call uses valid initial state even when refresh is slow. The
schedule survives Worker replacement without another Flow or goroutine. The
opaque shape remains provider-specific, but it is still a declared typed
Attribute rather than an arbitrary metadata map.
