# ADR 0005: Atomic question batches

## Status

Accepted on 2026-09-10.

## Context

An answer previously used the ordinary send operation. Snapshot could continue
to expose the prompt after HTTP acceptance because only the waiting Step closed
it. A single prompt and answer also prevented Codex-style review of several
related questions before one submission.

## Decision

`request_user_input` accepts one to three structured questions per batch. Each
question has a stable ID, short header, prompt, and two or three suggested
options. The first accepted tool call writes `PendingUserInput`; remaining tool
calls from that model response are canceled. Runtime validation prevents a
later call from replacing an unresolved batch. After resolution, later model
turns may create any number of additional batches.

The header is a navigation label limited to 32 Unicode characters. The limit is
part of the model schema, runtime validation, and public API contract. It is not
runtime configurable because those three boundaries must agree. Clients handle
narrow layouts by truncating the visible label while exposing its full text.

`AnswerQuestions` takes the pending call ID and one non-empty answer for every
question ID. The RPC locks `PendingUserInput`, validates the exact set, deletes
the batch, and publishes one typed value to `AnsweredUserInputs` in one atomic
commit. While the batch is pending, `AwaitUser` waits only for that Channel
instead of consuming steering or queued messages. Execute appends the answer to
`CurrentMessages`, emits `user_input_answered`, and enters `AnsweredInput`.
That Step starts the answer's model turn before either queue. The message
retains the answered call ID internally for Plan execution semantics.

`SendMessage` rejects while a batch is pending. The browser keeps answers only
in its current React session, allows backward navigation and edits, and submits
only when every question is answered. A selected option may include supplemental
text, which the browser submits as `option: details`; `Other` submits only its
required free text. The generated `answerQuestions` client is the only
pending-input mutation path.

## Consequences

HTTP `202` means the exact batch is closed and its answer is durably queued for
the waiting Agent Step. Duplicate, stale, partial, and mismatched answers return
`409` without changing the batch. Accepted answers do not enter the editable
message queue. The answer enters chat history when `AwaitUser.Execute` consumes
it. Its activity event lets the browser display the locally known answer before
the next Snapshot arrives.
