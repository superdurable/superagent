# Dex Server v0.11.2 upgrade

Status: released SDK and Server integration.

## Scope

Upgrade dexcli to `v0.11.2` and the Go SDK to `v0.11.2`. Components are
independently published even when their selected versions match. SuperAgent
does not opt into permission-based Work Queue projections in this release.

The Flow graph, durable resource identities, RPC definitions, retry policy,
and persistence schema remain unchanged. Existing Agent Flows continue through
the same registered definitions.

## Tests

The CLI installer pins the four checksums from the native `cli-v0.11.2`
`checksums.txt`, and `go.sum` pins the published SDK module. Deterministic
quality gates, Flow visualization, a real Server integration suite, and the
full-stack browser E2E validate the new combination before release.

## Documentation

README, contributing guidance, the Flow model, and deployment constraints now
name Server `v0.11.2`, Go SDK `v0.11.2`, and dexcli `v0.11.2` where relevant.

## UI/UX

No UI behavior changes. Browser E2E verifies Snapshot reconciliation, archive
pagination, tool recovery, keyboard behavior, and visible interaction state
against the upgraded Server.
