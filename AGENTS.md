# SuperAgent Engineering Rules

These rules apply to the entire repository. `AGENTS.md`, `CLAUDE.md`, and the
equivalent files under `.cursor/rules/` must remain semantically synchronized.

## Product boundary

SuperAgent is a production application built on the published Dex Go SDK. It is
not a Dex SDK example and must not depend on Dex internals.

- Use only released Dex server and Go SDK behavior. Never invent an API from a
  proposal, screenshot, branch, or unreleased source tree.
- Do not add a `go.mod` `replace` directive for Dex.
- Before launch, remove dead APIs and fields. Do not add compatibility shims,
  deprecated aliases, dual paths, or comments describing discarded behavior.

## Dex skill is mandatory

Before changing or reviewing Flow, Step, RPC, Attribute, AttributeMap, Channel,
ChannelMap, Stream, Timer, retry, recovery, or Dex Client code:

1. Load the installed `dex-developer` skill through the agent's native skill
   mechanism. Codex uses `$dex-developer`, Claude Code uses
   `/dex:dex-developer`, and Cursor uses the installed `dex-developer` skill.
2. Read every reference routed by the skill for the work being performed.
3. Treat the installed SDK source and version-matched runnable examples or
   real-server compile-contract tests as the API contract.

Do this again in every later turn that touches those concepts. Do not rely on a
previous turn's memory.

If the skill is unavailable, stop Dex-related changes and provide the
installation instructions at
https://docs.superdurable.io/build-with-ai/dex-developer-skill. SuperAgent does
not load the skill at application runtime.

## Snapshot boundary

`GET /products/ai-agent/snapshot` is the only durable current-interaction and
reconciliation read model. Do not add parallel Agent state read models. The
archive endpoint is an on-demand continuation over immutable application
history. Queue deletion and steering accept only message IDs from a Snapshot.
Application history means `CurrentMessages` and `ArchivedMessages`, never Dex
execution history.

## Dex application modeling

- Define Flow identity, input, output, Step graph, transitions, resources, RPCs,
  Streams, Timers, retries, and failure behavior before implementation.
- Keep one Flow per source file. Keep registrations, handlers, waits,
  transitions, resource access, and recovery directly visible to
  `dexcli visualize`.
- Do not commit generated Flow Definition JSON. Generate it in a temporary
  directory and reject every visualizer diagnostic in CI.
- Put durable wait conditions in `WaitFor`; put external side effects only in
  `Execute`.
- Treat each `WaitFor`, `Execute`, and RPC invocation as an independent Dex
  atomic commit boundary. Use stable idempotency keys for external effects.
- Name every RPC with an action verb. Read RPCs start with `Get` or `List`.
- Write a waiting status in the target Step's `WaitFor`, not in the preceding
  Step.
- Use Attributes for durable state and Channels for delivery, never as storage.
- Store application history in a typed AttributeMap with explicit sequence
  metadata. A queued message enters history only when consumed.
- Apply steered messages only at documented safe Step boundaries. Queued
  messages do not interrupt active work.
- Race durable Timers with the steered Channel without blocking the Worker.
- Commit compaction summaries and their covered sequence range before deleting
  compacted messages.
- Use the SDK buffered text writer for assistant and reasoning deltas. Keep
  structured activity in a separate Stream.
- Never persist, expose, or reconstruct hidden chain-of-thought.
- Treat Streams and BlobCache as disposable acceleration layers. Durable state
  must recover without either one.
- Do not export Dex resource getters merely to expose Channel, Stream,
  Attribute, or AttributeMap descriptors.

## Architecture and types

- Keep domain, application, transport, provider, MCP, and Dex adapter concerns
  in separate packages.
- Define interfaces in the consuming package. Do not manufacture abstractions
  for a single implementation.
- Inject required dependencies through constructors. Never add setter or
  post-construction wiring.
- Inject a pointer to the component's configuration section, not individual
  settings or the entire root config. Fail fast on a missing required section.
- Use distinct domain types for identifiers and sequences. Do not interchange
  IDs because their underlying representation matches.
- Represent closed string sets as validated string-backed enum types. Reject an
  unknown external value with a typed validation error; never panic.
- Domain packages must not use `any`, `interface{}`, `map[string]any`, untyped
  JSON objects, or raw string status values.
- Prefer concrete structs and explicit mappers at transport boundaries.
- Optional fields have intentional pointer or sum-type semantics. Do not blur
  missing, null, and zero values.
- Do not defensively deep-copy values. Copy only when code must independently
  mutate shared data.
- Every goroutine has one owner, a cancellation path, and a join path.
- Every response body and subprocess is closed or reaped on every path.

## Go

- Follow Effective Go, Go Code Review Comments, and standard library
  conventions unless a repository rule is stricter.
- Order handwritten files top-down: type, constructor, entry methods, handlers
  in dispatch order, state mutation, conversion, then small accessors.
- Keep one struct's method set together in one primary file.
- Put a compile-time interface assertion immediately after each handwritten
  concrete struct that intentionally implements an interface. Assert
  consumer-owned cross-package interfaces at the composition boundary when an
  adjacent assertion would reverse package dependencies. Exclude generated
  code.
- Use descriptive names. Receivers and `i`, `j`, `k`, `n`, `err`, `ctx`, `ok`,
  `t`, `mu`, `wg`, `id`, `r`, `w`, and `ch` may be short.
- Boolean variables, constants, and methods use predicate names such as `is`,
  `has`, `can`, `should`, or `supports` in idiomatic Go capitalization.
- Required dependencies fail at construction. Check `nil` only when it is a
  valid state.
- Every configuration field documents its default, unit, range, lifecycle, and
  operational effect where applicable.
- Return, wrap, log, or deliberately handle every error. Never silently discard
  an error.
- External and user-provided values fail gracefully. Trusted invariants may fail
  fast at the owning boundary.
- Alias imports only for collisions or misleading package names.
- Do not create generic `utils`, `helpers`, or `common` packages.
- Generated code is not manually edited and is excluded from style rewrites.

## TypeScript and React

- Enable all practical TypeScript strictness flags. Handwritten code must not
  contain `any`, unsafe casts, or duplicated transport models.
- Model UI state with reducers and discriminated unions. Make impossible states
  unrepresentable where practical.
- Keep effects single-purpose. Every request, long poll, and subscription owns
  an `AbortController`, cleanup, and stale-response guard.
- Do not suppress React Hooks or accessibility lint rules to ship a change.
- Keep components focused; split stateful page logic from presentation and
  generated API code.
- Preserve keyboard behavior, focus, loading, reconnecting, stale, terminal,
  failure, responsive, and reduced-motion states.

## OpenAPI

`api/openapi.yaml` is the only frontend/backend HTTP contract source.

- Generate the Go server, router, codecs, and validation with ogen.
- Generate the TypeScript Fetch client, models, and enums with Hey API.
- Commit generated output and require a zero-diff generation check.
- Never handwrite a transport interface already described by OpenAPI.
- Define optional, nullable, formats, bounds, discriminators, and enums
  precisely in the specification.
- Map generated transport types to domain types explicitly.
- Contract tests must assert the complete supported HTTP path set.

## Providers, MCP, and secrets

- Keep provider SDK types behind a typed provider boundary.
- Disable hidden provider retries and automatic tool execution. SuperAgent owns
  retry, approval, idempotency, and cancellation policy.
- MCP server configuration is trusted Worker configuration, never model output.
- Tool calls use stable call IDs. Unknown or malformed outcomes return typed
  errors.
- Never commit, print, snapshot, or attach `.env`, credentials, authorization
  headers, provider request bodies containing secrets, or decrypted tokens.
- Unit and default integration tests use deterministic fakes or local protocol
  fixtures. Real OpenAI tests load the root `.env` only through the explicit
  live-test target and never log the key.
- Logs use structured fields and explicit redaction. Do not log arbitrary model
  or MCP payloads at error level.

## Maintainability

- Lift a closure into a method when it captures three or more values, mutates
  outer state, has multiple call sites, or outlives one statement.
- New comments explain non-obvious reasons, trade-offs, invariants, or external
  constraints. Prefer clearer code over narration.
- Keep a new contiguous comment under 20 words unless the user requests detailed
  prose. Configuration and return-value documentation are exempt.
- Keep documentation direct. Put one idea in each sentence. Split behavior,
  constraints, and rationale into separate sentences.
- Do not introduce `NormalizeXyz` identifiers. Name the concrete operation,
  such as `ValidateAndSortSelections`, `TrimWhitespace`, or `CanonicalizeURL`.
- Any API retained solely for tests must include `ForTestOnly` in its identifier.
  Prefer placing it in a test-only source file when production code does not need it.
- Reuse an existing repository or public API term exactly. Do not invent a
  synonym for an established term such as instance, name, message, or
  definition.
- Preserve existing comments verbatim during refactors. Update only stale facts
  while retaining their meaning.
- Before creating a binary, add its exact path to `.gitignore` and
  `.dockerignore`.

## Tests and verification

- Run repository suites through Make targets, not ad hoc full-suite commands.
- Tee long-running suite output to a scoped file under `/tmp`.
- Do not skip, gate, weaken, or delete a failing assertion to make a suite pass.
- Every Agent behavior change or feature requires a released-Dex server
  integration test and a full-stack browser E2E test.
- Full-stack E2E uses the real HTTP API and asserts visible DOM state, focus,
  keyboard behavior, and user interactions. Do not replace the API with route
  fulfillment or stop at an HTTP success assertion.
- When a test reveals a product discrepancy, fix production code. Never change
  the expected behavior to preserve a bug.
- Async tests use unique Flow IDs and deadline-based polling. Do not use sleeps
  for convergence.
- Test Worker replacement at every durable wait and external-effect boundary.
- Run `dexcli visualize` after changing the Flow graph and fail on every
  diagnostic.
- Required gates include formatting, vet, static analysis, race tests,
  vulnerability checks, generated-code drift, TypeScript strict checking,
  type-aware lint, component tests, and browser E2E.

## Plans and documentation

Every implementation plan includes concrete `Tests`, `Documentation`, and
`UI/UX` sections. Use `N/A` only with a specific reason.

Keep these documents current with the code:

- `ARCHITECTURE.md` for package boundaries and durable/live reconciliation.
- `docs/flow-model.md` for Flow resources and application-history semantics.
- `docs/adr/` for consequential architecture decisions.
- `CONTRIBUTING.md` for local setup, generation, verification, and skill use.

## Git and license

- New feature branches start from the current `origin/main`; fetch before
  branching.
- End every turn that changes files with a meaningful commit and a clean working
  tree. Do not create empty commits.
- Never use `--no-verify` and never add Cursor or an agent as author, committer,
  co-author, or attribution trailer.
- After each commit, inspect `git log -1 --format='%an %ae%n%B'`.
- New or edited handwritten Go, TypeScript, JavaScript, CSS, HTML, shell, Python,
  and OpenAPI files use the repository Apache-2.0 header.
- Generated files and the vendored Dex skill follow their own recorded license
  and are excluded from header rewriting.
