# Superagent architecture

## System boundary

```text
React portal
    │ generated Fetch client
    ▼
ogen HTTP server ─────── best-effort events
    │ typed command mapping
    ▼
Dex Client ── FlowService ── Dex Worker
                                ├── provider adapter ── model API
                                ├── MCP registry ────── MCP server
                                └── BlobCache (disposable)
```

Dex durable resources are the source of application truth. HTTP handlers map
generated transport types to domain types and invoke commands; they do not own
Agent state. Streams reduce latency but never become recovery state.

## Package ownership

| Package | Owns | Must not own |
|---|---|---|
| `internal/agent` | Domain IDs/enums, Flow graph, private Dex descriptors, command client | Provider protocols, HTTP transport models, global configuration |
| `internal/api` | ogen implementation, validation mapping, problem responses | Handwritten routes, generated-model duplicates, durable state |
| `internal/app` | Dependency construction, goroutine ownership, startup and shutdown | Domain decisions or provider-specific payloads |
| `internal/config` | Environment parsing and validated immutable sections | Runtime singletons or secret logging |
| `internal/model` | Provider routing, protocol adapters, in-memory credential lookup | Dex resources or HTTP API responses |
| `internal/mcp` | Trusted server config, discovery, policy, sessions, retries, brokers | Agent state transitions or exported Dex resource access |
| `internal/webui` | Immutable embedded assets and security headers | API fallback logic or server-side UI state |
| `web` | React portal and generated Fetch client | Handwritten API response types or durable-state reconstruction |

Interfaces live at their consuming boundary. Concrete single-use components do
not receive speculative interfaces, and there is no general-purpose helpers
package.

## Durable Agent model

One stable `FlowID` identifies one conversation. `AgentMessages` is the typed
application history; it is not Dex execution history. `AgentState` owns the
retained sequence range, interaction mode, status, pending tool cursor, and plan
revision. Plans, pending approvals, timers, input prompts, and cumulative context
summaries are separate typed Attributes.

Queued and steered user messages use distinct Channels. A queued message enters
application history only after a Step consumes it. Steering is consumed only at
explicit safe Step boundaries, so it cannot claim to cancel an in-flight model
or MCP side effect. Approval and plan execution use ChannelMaps keyed by typed
call ID and plan revision.

Each `WaitFor`, `Execute`, and RPC invocation is an independent Dex atomic commit
boundary. Waiting state is written in the `WaitFor` that establishes the wait.
Provider and MCP calls occur only in `Execute`. The complete graph and resource
table are in `docs/flow-model.md`.

## Durable and live reconciliation

| Data | Durability | Recovery role |
|---|---|---|
| Attributes, AttributeMaps, Channels | Dex durable state | Authoritative |
| assistant/reasoning buffered Streams | Best effort | UI latency only |
| structured activity Stream | Best effort | Observability and UI latency |
| BlobCache | Disposable local acceleration | Never authoritative |
| provider/MCP session | Per invocation | Recreated after failure |
| generated browser state | In-memory projection | Replaced from Snapshot in Phase 2 |

Phase 1 exposes command writes and event reads but intentionally does not expose
an incomplete durable read model. Phase 2 will perform one generated
`GET /products/ai-agent/snapshot`, atomically replace the React reducer's durable
view, then apply event deltas. A disconnect, resume-token gap, or stale optimistic
mutation will trigger another Snapshot. The four legacy reads will never be
introduced.

## HTTP contract

`api/openapi.yaml` is the sole contract source. ogen generates the Go server,
router, codecs, and validation. Hey API generates the TypeScript Fetch client,
models, and enums. Explicit mappers keep generated transport types out of the
domain package.

Phase 1 serves only portal metadata, Flow start, message commands, plan
execution, tool approval, events, health, and readiness. The embedded React
files are served at fixed paths with a restrictive Content Security Policy,
ETags, method checks, and no dynamic file-system lookup.

## Providers and credentials

The provider-neutral model boundary accepts typed messages, tools, and callback
writers. OpenAI uses the official Responses streaming SDK. Anthropic, Gemini,
and Groq adapters use strict native protocol structs. Provider redirects are
disabled, response bodies are closed, attempts are bounded by context and HTTP
timeouts, and unknown provider values return typed validation errors.

API keys come from process configuration or an in-memory Flow override. They are
never included in Dex configuration, model messages, activity events, or log
fields. Base URL overrides require trusted absolute HTTPS URLs without embedded
credentials, queries, or fragments.

## MCP safety

MCP server YAML is trusted operator configuration with unknown fields rejected.
stdio commands run directly without a shell and receive a minimal environment.
Streamable HTTP receives only explicitly mapped headers and uses an owned
transport with SDK retries disabled.

Discovery follows every pagination cursor and publishes tools atomically only
after all configured servers succeed. Unknown and write-capable tools require
approval and one attempt by default. Only explicitly trusted read-only tools can
retry; each attempt and total retry duration are bounded. Tool-level `isError`
is a completed known failure and is not blindly retried. Every session is closed,
stdio subprocesses are reaped, and idle HTTP connections are closed by the
registry owner.

## Process lifecycle

`internal/app` owns every long-lived resource. Startup validates configuration,
discovers MCP, constructs providers, opens BlobCache, starts the Worker, waits
for its listener, marks readiness, and then serves HTTP. Any startup failure
closes everything already constructed.

On cancellation or an unexpected Worker/HTTP exit, readiness is cleared. The
HTTP server and Worker receive bounded shutdown contexts, their goroutines are
joined, and the Dex Client, BlobCache, MCP registry, and provider transport are
closed exactly once. No background goroutine is intentionally orphaned.
