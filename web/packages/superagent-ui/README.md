# @superdurable/superagent-ui

Transport-free React components for building SuperAgent conversation surfaces.
The package owns presentation and local interaction behavior only. Applications
remain responsible for API calls, durable state, routing, retries, and mapping
their domain objects into the exported view models.

## Install

```bash
npm install @superdurable/superagent-ui react
```

React 18.2 and React 19 are supported through the package's peer dependency.
Import the stylesheet once from the consuming application:

Each GitHub Release also provides a versioned npm-compatible archive. Use the
matching version when the npm registry package is unavailable:

```bash
npm install https://github.com/superdurable/superagent/releases/download/v0.1.0/superdurable-superagent-ui-0.1.0.tgz react
```

The installed package keeps the name `@superdurable/superagent-ui`, so imports
remain unchanged.

```tsx
import {
  ActivityRow,
  ApprovalCard,
  ConversationComposer,
  ConversationTimeline,
  MarkdownContent,
  PendingMessageQueue,
  PendingQuestionBatch,
  PlanPanel,
  TimerCard,
  ToolCallCard,
  ToolRecoveryPanel,
  buildConversationTimeline,
  pairToolCallsById,
  planActionPresentation,
  useTimelineFollow,
} from "@superdurable/superagent-ui";
import "@superdurable/superagent-ui/styles.css";
```

## Conversation timeline

`buildConversationTimeline` merges durable messages, consumed-user projections,
reasoning (when the embedding backend enables it), activities, and the live
assistant into one chronologically ordered list. `ConversationTimeline` applies that order and renders through slots
(`renderMessage`, `renderActivity`, `renderReasoning`, `renderAssistant`,
`renderConsumedUser`) so Studio can keep its own markup while sharing sort
logic.

`buildConversationPresentation` and `ConversationView` provide the higher-level
conversation-first surface. Durable Messages define turns, model invocations,
tool calls, results, repeated calls, and completed timing. Snapshot pending
state supplies current waits; best-effort activity and assistant text Streams
only enrich the view with progress, attempts, failures, and live output. The
optional reasoning input exists for legacy or explicitly enabled producers;
the presentation does not require a reasoning Stream. Every turn has one
collapsed Work log, so losing retained Stream events never destroys the durable
conversation structure.

Activity with a durable `messageSequence`, including input-consumption hints,
belongs to that message's turn instead of the global historical section.
Unanchored input-consumption hints remain reconciliation-only and are not shown.
While execution is live, work activity at or after the latest durable message
remains in that current turn even when its durable assistant message has not
reached the Snapshot yet. Unanchored live tool events render below that turn
until durable Work log data replaces them. Terminal unanchored activity remains
in the global historical section.

`ConversationTurn.operations` preserves causal order: each model operation is
followed by the tools it requested. Adjacent identical calls in one model batch
may collapse, but repeated calls separated by another model stay in place.
`models` and `tools` remain compatibility projections.

`ConversationTurn.questions` safely projects durable `request_user_input`
calls. `TimelineMessage.answeredInputCallId` associates the later user answer;
failed tool results remain visible with a failed status, while malformed and
legacy rejected calls without a result are shown as failed without exposing raw
tool JSON.
Use `renderQuestion` to customize that card.

`ConversationView` owns message alignment and timestamp disclosure even when a
consumer supplies `renderMessage`. User messages are compact, right-aligned
bubbles; assistant messages remain left-aligned. Hovering or focusing either
message reveals its local timestamp below the content.

`ToolRecoveryPanel` moves programmatic focus to the recovery region when it
appears. The existing warning card communicates that focus without drawing a
browser-default input outline around its heading.

Each affected call uses a compact card with formatted, height-bounded arguments
and two explicit recovery choices. The disclosure keeps a visible inset focus
indicator without resembling a text input.

Completed model and tool duration is `createdAt - startedAt`. Missing or
negative legacy timing is omitted. Running durations update once per second but
are excluded from live-region announcements. Stream timestamps remain ordering
metadata and are never rendered as stream duration.

Collapsed Work logs show separate succeeded and failed operation counts. Active
and unknown counts appear when those states are present, so one failed operation
does not make the entire log look unsuccessful.

Set `isExecutionLive={false}` for terminal history. Unpaired legacy calls then
show `unknown` and do not start the elapsed-time interval. Collapsed Work logs
do not mount tool result bodies or call `renderToolCall` until expanded.

## Tool call cards

`pairToolCallsById` joins assistant `toolCalls` to `role=tool` messages by call
id. `ToolCallCard` collapses request and result by default:

- `apply_patch` → red/green patch diff from the request `patch` field
- `exec_short_command` / `exec_long_command` → highlighted `$` command plus grey
  stdout/stderr (ANSI stripped)
- other tools → generic JSON request/result

Override with `renderToolCall` when a product needs a custom row.

## Shared chrome and helpers

Also exported: `ActivityRow`, `PlanPanel` / `planActionPresentation`,
`ApprovalCard`, `TimerCard`, `mergeSequencedMessages`, `mergeActivityEvent`,
live-text helpers, and `useTimelineFollow` (optional element scroll root for
panel scrollers).

`ConversationComposer` is controlled. `PendingMessageQueue` receives plain view
models and reports semantic actions by item ID, so it has no knowledge of Dex,
HTTP, generated clients, or product-specific stores. `MarkdownContent` renders
GitHub Flavored Markdown without enabling raw HTML.

`PendingQuestionBatch` handles one-to-three-question navigation, predefined and
free-form answers, local draft review, and one atomic ordered `onSubmit` call.
Navigation labels use an ellipsis in narrow layouts and expose the complete
header through a native tooltip.
The application keeps ownership of the durable call ID and transport request.
Key the component by that stable batch identity when replacing one pending batch
with another so React resets its local drafts.

`ToolRecoveryPanel` renders one complete manual recovery batch, manages the
per-call retry or continue selections, moves focus to the recovery heading, and
reports one atomic resume or stop decision. The application maps that semantic
decision to its generated transport types and reconciles from its Snapshot.

All owned selectors use the `sa-` prefix. Consumers can theme the components
with the documented `--sa-*` custom properties in `styles.css`. A few legacy
class names remain in the markup for compatibility with the SuperAgent browser
application's stable full-stack test selectors; consumers should target the
namespaced classes for styling.
