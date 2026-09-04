# SuperAgent

SuperAgent is a production-grade AI agent built on [Dex](https://github.com/superdurable/dex)
durable execution and the released Dex Go SDK. It combines a Go API and Worker
with an independently deployable React application. OpenAPI generates both
transport boundaries from one contract.

## Capabilities

- Durable conversations with typed application history
- Streaming assistant text and provider-authored reasoning summaries
- Plans, tool approvals, user input, steering, and durable timers
- OpenAI, Anthropic, Gemini, Groq, and deterministic mock providers
- MCP over stdio and Streamable HTTP
- Context compaction and Worker replacement recovery
- Atomic Snapshot restoration with best-effort live event reconciliation
- Separately deployable backend and frontend artifacts

The browser restores durable state through one
`GET /products/ai-agent/snapshot` request. It applies Stream updates for low
latency and reconciles from Snapshot after reconnects, mutations, and detected
gaps. The retired history, queue-read, describe, and status routes do not exist.

## Architecture

```text
React application ── generated Fetch client ──> Go OpenAPI API
                                                     │
                                                     ├── Dex Client
                                                     └── Dex Worker
                                                              │
                                                              ├── model providers
                                                              └── MCP servers
```

The Go process never embeds or serves frontend assets. `web/dist` reads its API
origin from `config.json`, so frontend deployments can change independently of
the backend.

See [ARCHITECTURE.md](ARCHITECTURE.md) for package boundaries and durable/live
reconciliation. See [docs/flow-model.md](docs/flow-model.md) for the Flow graph
and resource model.

## Prerequisites

- Go matching [`go.mod`](go.mod)
- Node.js and npm compatible with [`web/package-lock.json`](web/package-lock.json)
- A reachable Dex server
- A writable directory for disposable Dex BlobCache data

## Quick start

Install frontend dependencies and build both deployable artifacts:

```bash
npm --prefix web ci
make build-api
make build-web
```

Start a compatible Dex server. Then run the API and Worker:

```bash
SUPERAGENT_HTTP_ALLOWED_ORIGINS=http://127.0.0.1:3000 ./bin/superagent
```

Serve the frontend from a separate terminal:

```bash
python3 -m http.server 3000 --directory web/dist
```

Open `http://127.0.0.1:3000/`. The local defaults are:

| Component       | Address                  |
| --------------- | ------------------------ |
| Frontend        | `http://127.0.0.1:3000/` |
| API             | `http://127.0.0.1:8080/` |
| Dex FlowService | `127.0.0.1:8801`         |
| Dex Worker      | `127.0.0.1:8803`         |

Use HTTPS for production deployments so browser secure-context APIs remain
available.

## Configuration

| Variable                          | Purpose                            | Default                      |
| --------------------------------- | ---------------------------------- | ---------------------------- |
| `SUPERAGENT_HTTP_ADDRESS`         | OpenAPI bind address               | `127.0.0.1:8080`             |
| `SUPERAGENT_HTTP_ALLOWED_ORIGINS` | Exact comma-separated CORS origins | none                         |
| `DEX_FLOW_SERVICE_ADDRESS`        | Dex FlowService address            | `127.0.0.1:8801`             |
| `DEX_WORKER_BIND_ADDRESS`         | Local Worker bind address          | `127.0.0.1:8803`             |
| `DEX_WORKER_TARGET`               | Worker address advertised to Dex   | Worker bind address          |
| `DEX_BLOB_CACHE_DIR`              | Disposable BlobCache directory     | `/tmp/superagent-blob-cache` |
| `DEX_BLOB_CACHE_MAX_BYTES`        | BlobCache size limit in bytes      | `536870912`                  |
| `DEX_AGENT_MCP_CONFIG`            | Trusted MCP YAML path              | disabled                     |
| `OPENAI_API_KEY`                  | OpenAI credential                  | unset                        |
| `ANTHROPIC_API_KEY`               | Anthropic credential               | unset                        |
| `GEMINI_API_KEY`                  | Gemini credential                  | unset                        |
| `GROQ_API_KEY`                    | Groq credential                    | unset                        |

Each provider accepts a trusted HTTPS origin override named
`<PROVIDER>_BASE_URL`. Provider credentials stay in Worker memory and are never
persisted in Dex state or logged. Copy
[`web/mcp-servers.example.yaml`](web/mcp-servers.example.yaml) to configure
trusted MCP servers.

For a cross-origin frontend deployment, add its exact origin to
`SUPERAGENT_HTTP_ALLOWED_ORIGINS`. Wildcards and credentialed cross-origin
requests are intentionally unsupported. Serve `config.json` with
`Cache-Control: no-store` and cache fingerprinted frontend releases instead.

## Development

OpenAPI is the only HTTP contract source. Regenerate and verify both clients:

```bash
make generate
make check-generated
```

Run the complete credential-free quality gate:

```bash
make check
```

Run real-server and provider verification explicitly:

```bash
DEX_FLOW_SERVICE_ADDRESS=127.0.0.1:8801 make test-dex-integration
make test-openai-live
```

Only `make test-openai-live` reads `OPENAI_API_KEY` from the ignored root
`.env`. Default tests use deterministic fakes or local protocol fixtures.

## Flow visualization

Generate and verify the checked-in Go Flow Definition Graph:

```bash
make generate-flow-definition
make check-flow-definition
```

Render the Go source directly, or serve all generated definitions:

```bash
make flow-visualize
make flow-render
```

`make flow-render` starts a local Dex development environment. Select
**Flow Rendering** in the printed Dex Web address. Restart it after regenerating
definitions.

## Project documentation

- [CONTRIBUTING.md](CONTRIBUTING.md): setup, generation, and verification rules
- [ARCHITECTURE.md](ARCHITECTURE.md): package and deployment boundaries
- [docs/flow-model.md](docs/flow-model.md): Flow resources and transitions
- [MIGRATION.md](MIGRATION.md): completed migration evidence
- [docs/python-go-parity.md](docs/python-go-parity.md): immutable Python parity baseline

Read [`AGENTS.md`](AGENTS.md) before making changes. Work involving Dex Flows,
Steps, RPCs, Channels, Streams, Timers, retries, or recovery must also follow
the vendored [`skills/dex-developer`](skills/dex-developer/SKILL.md) guidance.
