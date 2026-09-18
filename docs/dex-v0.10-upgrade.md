# Dex Server v0.10.0 upgrade

Status: implementation and verification in progress.

## Scope

Upgrade Server and CLI to v0.10.0 and Go SDK to v0.9.1. The SDK explicitly
registers RPCs and fixes their execution options at registration. It removes
invocation-specific selective loads, including the former single-instance load
used by `GetArchivedMessages`.

The Server lock uses its immutable compatibility manifest. The SDK-only patch
has no Server manifest, so its lock records the release tag, source commit, and
Go module checksums. Validation also requires the SDK and Server protocol
intervals to overlap.

## Release prerequisite

The missing Server v0.10.0 manifest was backfilled after the Dex partial-release
workflow was corrected. SuperAgent pins that asset and its SHA-256. The
`sdk-go/v0.9.1` release is pinned independently because compatibility manifests
are Server release contracts and the patch published only the Go SDK.

## Tests

The published CLI v0.10.0 archive checksums and SDK v0.9.1 module checksums are
locked. Unit compilation verifies the explicit RPC registration API. Real
Server integration, visualization, complete checks, and browser E2E must pass
before release.

Server implementation and real Temporal integration verify that a non-locking,
non-transactional read-only RPC uses Query and remains readable after Flow
closure. Snapshot therefore performs one RPC without visibility lookup,
completion wait, or lifecycle-error retry. Continue-as-new remains a direct RPC
regression gate.

## Documentation

CONTRIBUTING documents the mixed Server/SDK lock. The Flow model and ADR 0014
document immutable registered RPC options and the whole-map archive load imposed
by the v0.9.1 contract.

## UI/UX

No controls change. Verify that post-command Snapshot reconciliation restores
the composer, and that archive pagination, scrolling, focus, and keyboard
behavior pass through the real HTTP API.
