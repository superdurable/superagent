# Superagent

Superagent is a standalone, production-oriented AI agent built on Dex durable
execution. The current Phase 1 implementation ports the Python agent core to Go
on the released Dex Go SDK `v0.2.9`, retains the React application, and uses
OpenAPI-generated server and browser contracts.

The immutable upstream Python baseline is under `reference/python/`. Migration
scope, external gates, and evidence are tracked in `MIGRATION.md`; package and
durability boundaries are described in `ARCHITECTURE.md`.

## Phase 1 scope

Phase 1 includes typed Agent state and commands, plans, approvals, user input,
durable timers, steering, compaction, buffered streams, provider adapters, MCP
stdio and Streamable HTTP transports, an embedded launch portal, and graceful
process shutdown.

The conversation read surface deliberately waits for the released Dex Snapshot
API. There are no temporary history, queue-read, describe, status, Snapshot, or
queue-mutation HTTP routes. The launch portal makes that boundary explicit.

## Prerequisites

- Go matching `go.mod`
- Node.js and npm compatible with `web/package-lock.json`
- A reachable Dex server for running the application or integration suite
- A writable local directory for Dex BlobCache data

## Build

```bash
npm --prefix web ci
make generate
make check-generated
make build
```

Generated ogen and Hey API files are committed. `api/openapi.yaml` is their
only source; do not hand-edit generated transport types or routes.

## Run

Start a Dex server, then run:

```bash
./bin/superagent
```

The default addresses are:

- Portal: `http://127.0.0.1:8080/products/ai-agent/`
- Dex FlowService: `127.0.0.1:8801`
- Dex Worker: `127.0.0.1:8803`

Loopback development origins are suitable for the browser-generated Flow IDs.
Serve production deployments over HTTPS so browser secure-context APIs remain
available.

## Configuration

| Variable | Purpose | Default |
|---|---|---|
| `SUPERAGENT_HTTP_ADDRESS` | OpenAPI and portal bind address | `127.0.0.1:8080` |
| `DEX_FLOW_SERVICE_ADDRESS` | Dex FlowService address | `127.0.0.1:8801` |
| `DEX_WORKER_BIND_ADDRESS` | Local Worker bind address | `127.0.0.1:8803` |
| `DEX_WORKER_TARGET` | Worker address advertised to Dex | Worker bind address |
| `DEX_BLOB_CACHE_DIR` | Disposable local BlobCache directory | `/tmp/superagent-blob-cache` |
| `DEX_BLOB_CACHE_MAX_BYTES` | BlobCache byte limit | `536870912` |
| `DEX_AGENT_MCP_CONFIG` | Trusted MCP YAML path | disabled |
| `OPENAI_API_KEY` | Process-level OpenAI credential | unset |
| `ANTHROPIC_API_KEY` | Process-level Anthropic credential | unset |
| `GEMINI_API_KEY` | Process-level Gemini credential | unset |
| `GROQ_API_KEY` | Process-level Groq credential | unset |

Each provider also accepts a trusted HTTPS origin override named
`<PROVIDER>_BASE_URL`. Secrets stay in Worker memory and are never persisted in
Dex attributes or logged. Copy `web/mcp-servers.example.yaml` when configuring
MCP; environment mappings name secret sources rather than embedding values.

## Verification

```bash
make check
DEX_FLOW_SERVICE_ADDRESS=127.0.0.1:8801 make test-dex-integration
make test-openai-live
DEX_REPO=/absolute/path/to/dex make flow-visualize
```

`make check` is deterministic and credential-free. The Dex integration target
requires a running server. The explicit live target is the only test that reads
`OPENAI_API_KEY` from the ignored root `.env`.

Read `AGENTS.md` before changing the repository. Every Dex Flow, Step, RPC,
Channel, Stream, Timer, retry, or recovery change must also follow the vendored
`skills/dex-developer` skill and its routed references.
