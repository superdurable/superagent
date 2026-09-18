# Dex Server v0.10.0 upgrade

Status: released SDK and Server integration.

## Scope

Upgrade Server and CLI to v0.10.0 and Go SDK to v0.10.0. The SDK explicitly
registers RPCs and fixes their execution options at registration. It removes
invocation-specific selective loads, including the former single-instance load
used by `GetArchivedMessages`.

Components are independently published. SuperAgent directly pins the released
Go SDK in `go.mod` and dexcli in its installer. The CLI installer verifies native
release checksums. Application upgrades use ordinary pull requests.

## Tests

The published CLI v0.10.0 archive checksums and SDK v0.10.0 module checksums are
locked. Unit compilation verifies the explicit RPC registration API. Real
Server integration, visualization, complete checks, and browser E2E must pass
before release.

Server implementation and real Temporal integration verify that a non-locking,
non-transactional read-only RPC uses Query and remains readable after Flow
closure. Snapshot therefore performs one RPC without visibility lookup,
completion wait, or lifecycle-error retry. Continue-as-new remains a direct RPC
regression gate.

## Documentation

CONTRIBUTING documents component pins and the local version updater. The Flow model and ADR 0014
document immutable registered RPC options and the whole-map archive load imposed
by the v0.10.0 contract.

## UI/UX

No controls change. Verify that post-command Snapshot reconciliation restores
the composer, and that archive pagination, scrolling, focus, and keyboard
behavior pass through the real HTTP API.
