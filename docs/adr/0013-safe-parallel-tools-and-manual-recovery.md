# ADR 0013: Bound safe tool parallelism and stop on unknown outcomes

## Status

Accepted on 2026-09-17.

## Context

Models can return several independent tool calls in one response. Serializing
every known read delays coding-agent turns. Automatically continuing after an
ambiguous external failure can also make the model act on state that was never
confirmed.

MCP `isError=true` has different semantics. It is a completed, known failure
with a usable result, so retrying it or stopping before the model can correct it
is usually counterproductive.

## Decision

SuperAgent enables provider parallel tool-call generation. Only contiguous
calls whose trusted definitions explicitly support parallel execution are
scheduled together. The Agent-level limit defaults to four and can be set from
one through 32. Approval, write, user-input, and durable-wait calls are barriers.

Each parallel branch performs only its external call and publishes one typed
result. A durable join waits for every started branch and commits history in the
model's original order.

Known failures continue to the model without retry. Returned unknown outcomes
and exhausted Dex retries default to a durable manual-recovery boundary. The
user must retry or continue each failed call, or stop the current tool sequence.
A trusted per-tool `continue_with_unknown` policy preserves automatic progress
where operators explicitly accept it.

Recovery state contains the call ID, tool name, arguments, and redacted error
type. External error details are not persisted in Snapshot or activity events.
Recovery commands use the exact recovery revision and one Attribute lock, so a
recovery decision and steering cannot both commit.

## Consequences

Independent reads overlap while externally consequential operations remain
ordered. Progress activity may be interleaved, but durable model context is
deterministic.

Ambiguous failures stop coding agents by default and survive Worker replacement.
Retry remains at-least-once: it reuses the call ID, but an MCP server that does
not deduplicate may repeat an external effect.

The Flow adds branch, join, preparation, and manual-wait Steps plus two typed
ChannelMaps and one pending-recovery Attribute. Existing open executions retain
the older `RecoverToolExecution` Step.
