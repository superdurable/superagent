# Dex Server v0.10.0 upgrade

Status: blocked before changing the production release lock.

## Scope

Upgrade Server and CLI to v0.10.0. Retain Go SDK v0.9.0 for this Server-only
change. SDK v0.10.0 removes invocation-specific RPC options, including the
single-instance archive load used by GetArchivedMessages. Its migration needs
a separate design that preserves bounded history reads.

The new `--server-only` upgrade option preserves the SDK's original immutable
manifest. Release validation verifies both manifests and their protocol overlap.

## Release prerequisite

The [v0.10.0 publication](https://github.com/superdurable/dex/actions/runs/35303789151)
succeeded, but skipped its compatibility manifest job. That job requires every
SDK to be selected for publication. Java, Python, Rust, and TypeScript were
unchanged and skipped. The Server release therefore has no
`dex-compatibility-v0.10.0.json` asset.

The audited upgrade needs that official manifest and its SHA-256. Keep the
current release pins until partial-component releases can publish a manifest
recording the actual versions of all components.

## Tests

Verified the published CLI v0.10.0 archive checksum and started its embedded
Server using isolated databases. Go SDK v0.9.0 passed Agent and HTTP integration
tests against that Server. Flow visualization completed without diagnostics.
Server-only updater tests, formatting, workflow lint, Agent unit tests, and vet
passed.

Browser E2E exposed a read-only Snapshot long-poll expiry returning HTTP 503.
Snapshot now retries that typed error within its existing three-attempt budget.
The affected browser reconciliation scenario passed after the fix. The last
full E2E run passed 18 of 19 tests; the archive-history scenario still timed
out. Preserve that failure and resolve it before release. The run log is
`/tmp/superagent-dex-v010-e2e-retry-fix.log` on the verification host.

After the manifest is available, update the immutable pins, validate the release
lock, rerun real-Server integration and the complete browser suite, and commit
the final version change before publishing SuperAgent v0.4.0.

## Documentation

CONTRIBUTING documents Server-only manifest validation. The Flow model documents
bounded Snapshot retry behavior. Update the prerequisites and version references
when the release lock can be finalized.

## UI/UX

No controls change. Verify that post-command Snapshot reconciliation restores
the composer, and that archive pagination, scrolling, focus, and keyboard
behavior pass through the real HTTP API.
