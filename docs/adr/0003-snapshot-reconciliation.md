# ADR 0003: Reconcile durable state with one Agent Snapshot

## Status

Accepted on 2026-09-03.

## Context

History, description, and two pending-message Channels must represent one Dex
RPC invocation. Reading them through separate browser requests can expose state
from different commit boundaries. Streams reduce latency but are disposable and
cannot restore state after refresh or a resume-token gap.

Dex Go SDK `v0.2.11` supports explicit RPC projections for an AttributeMap and
pending Channel values. The version-matched real-server contract is
`sdk-go/integ/rpc_selective_state_test.go` at Dex commit `2f961961`.

## Decision

`Snapshot` is one read-only Dex RPC. Its client projection loads the complete
`AgentMessages` AttributeMap and pending values from `QueuedUserMessages` and
`SteeredUserMessages`. The handler returns the invocation Run ID and a concrete,
typed domain value. It does not consume, lock, or mutate projected resources.

Application history comes only from `AgentMessages`; Dex execution history is
not an application data source. The HTTP API exposes only
`GET /products/ai-agent/snapshot`, never the four earlier read routes.

The React reducer replaces the complete durable view with one Snapshot action.
Assistant, reasoning-summary, and activity Streams add low-latency updates.
Disconnects, activity boundaries, and command completion trigger a new Snapshot
for reconciliation. Queue mutations use stable message IDs from Snapshot.

## Consequences

Initial page state cannot tear across independent reads. Refresh and Worker
replacement recover without Streams or BlobCache. Snapshot currently loads the
whole retained application-history map before applying its bounded page because
the atomic map projection is the released SDK contract; retention and
compaction bound that cost.

RPCs target active Flows. Missing and terminal Flows use typed SDK-derived HTTP
errors rather than locally reconstructed terminal Snapshots. Queue mutation
conflicts cause another Snapshot instead of accepting stale browser state.
