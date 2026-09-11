# ADR 0009: Bound Stream recovery and use application RPCs

## Status

Accepted on 2026-09-11.

## Context

Starting a Stream read without a resume token replays from the retained head.
That makes browser refresh cost grow with retained best-effort data. Dex Go SDK
v0.6.1 provides `ListStreamMessages` and removes generic Client operations for
Attributes, AttributeMaps, Channels, and ChannelMaps.

## Decision

Refresh lists one newest-first Dex page per Agent Stream. SuperAgent reverses
each page into chronological order, applies at most the configured recovery
limit, and resumes the long poll from the newest returned token. The limit
defaults to 1000 and accepts values from 1 through 1000.

Application state access uses verb-first Flow RPCs. Public reads are
`GetSnapshot` and `GetArchivedMessages`; queue deletion is
`DeleteQueuedMessage`. Snapshot remains the only durable current-interaction
and reconciliation model. Integration-only resource probes are verb-first RPCs
whose identifiers end in `ForTestOnly`.

## Consequences

Refresh work is bounded independently of Stream retention. Older or trimmed
Stream hints are not reconstructed, and Snapshot remains authoritative after
ambiguous results or event loss. SuperAgent no longer depends on removed Dex
Client resource APIs. Renamed RPCs and Client methods intentionally replace the
pre-release noun-only names.
