# Python-to-Go parity and cutover

## Immutable source baseline

The final parity review uses Dex merge commit
`d09a5d7b75754d7b8df00bc06df5902507fc5425`, which merged Dex PR 448 on
2026-09-03. The application was compared directly with these upstream blobs:

| Git blob | Upstream file |
|---|---|
| `161c43dc9e1ebccccfe9a6be79046f78d862c46e` | `ai_agent_flow.py` |
| `ccd0e400b2dc61fb16246ba1bbaf219932b10f24` | `model_client.py` |
| `e19e31a13eed4ef82f45a0b52fd89a375379bd28` | `models.py` |
| `cf2ff32a200b3b3aef92c27f2e7f7447ba460df8` | `mcp_registry.py` |
| `cccc4428323e8b21141ab9be30790659cc8b5360` | `http_routes.py` |
| `4a2ddf7b3676ae6fe1266078ce9c0a02f9c6288b` | `ai-agent/src/App.tsx` |
| `c8bda2031ff54ed60a56ac9a6d92fadc8806bb6d` | `tests/unit/test_ai_agent.py` |
| `7e95612b7911136d748ca75416318ef0b85e6302` | `tests/integ/test_ai_agent.py` |

The full paths remain available from that immutable Git commit. Superagent does
not vendor the files after cutover.

## Behavioral parity

| Capability | Go implementation | Verification |
|---|---|---|
| Static Agent Step graph and recovery | `internal/agent/flow.go` | Dex integration and checked-in Flow Definition |
| Typed durable state and enums | `internal/agent/types.go`, `schema.go` | domain tests, exhaustive lint, decoder fuzzing |
| Ordered application history | `AgentMessages` AttributeMap | Snapshot pagination and provenance integration checks |
| Stateless provider context | provider-neutral messages and opaque OpenAI reasoning items | OpenAI protocol fixtures and live Responses API test |
| Mock, OpenAI, Anthropic, Gemini, and Groq | direct typed adapters under `internal/model` | deterministic provider protocol fixtures |
| Planning, revision checks, execution, and clearing | durable `AgentPlan` plus revisioned command | plan parity and Worker-replacement integrations |
| Approval and user input | durable Attributes plus typed Channels | approval, choices, multi-call cancellation, and restart integrations |
| Durable waits and steering | Timer raced with steered messages | timer replacement, ID-only steering, and batch-order integrations |
| Tool safety | per-session allowlists and approval-aware retry policy | Agent, registry, timeout, and retry tests |
| MCP tools, resources, and prompts | trusted stdio and Streamable HTTP registry plus brokers | discovery, paging, execution, cleanup, and protocol tests |
| Atomic browser recovery | one read-only Snapshot RPC plus three best-effort Streams | real Dex Snapshot and browser reconciliation tests |
| React conversation experience | reducer state and generated Fetch client | Vitest, accessibility lint, production build, and Playwright |
| Independent deployment | Go binary and static frontend artifact | backend route isolation and cross-origin browser tests |

## Intentional production differences

Superagent does not preserve example-only implementation choices:

- Go uses validated domain enums and distinct identifier types instead of raw
  Python strings.
- OpenAPI generates the HTTP server and browser client. Handwritten transport
  models are not retained.
- Direct provider adapters replace LiteLLM so retry, tool, credential, and
  cancellation policy remain application-owned.
- Snapshot is the only durable browser read model. The legacy history,
  message-queue read, describe, and status HTTP paths do not exist.
- Terminal Snapshots include typed Flow lifecycle and failure metadata.
- Frontend and backend are separate release artifacts.

These differences narrow ambiguous states or improve recovery. They do not
remove a supported user workflow from the upstream application.

## Deletion gate

The vendored Python oracle became removable only after all of these conditions
were satisfied:

1. Dex PR 448 merged and Dex Go SDK `v0.2.12` was released.
2. Phase 1 and Phase 2 behavior passed real-server integration.
3. The upstream Python tests were mapped to equal or stronger Go tests.
4. Snapshot, queue mutation, event reconciliation, and terminal recovery passed.
5. The live OpenAI Responses API test passed with the ignored local `.env`.
6. Deterministic Go, TypeScript, React, OpenAPI, race, fuzz, vulnerability, and
   Flow visualization gates passed.
7. Release artifacts were proven independent of the Python source.

`make check-cutover` prevents the deleted oracle or an operational dependency
on its old path from returning.
