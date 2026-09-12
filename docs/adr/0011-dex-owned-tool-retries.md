# ADR 0011: Let Dex own external tool retries

## Status

Accepted on 2026-09-11.

## Context

The MCP registry previously maintained its own attempt loop, deadlines,
backoff, and sleep. Those retries were invisible to the durable Flow graph and
could not recover correctly across Worker loss.

## Decision

`ToolDefinition` policy is applied to `ExecuteToolWithRetry` through movement
StepOptions. Each registry call performs one external attempt. Known business
failures return a normal error result. Transient or ambiguous failures return a
Go error and use Dex retry. Exhaustion routes to `RecoverToolExecution`, which
records one unknown outcome and continues the Agent.

Approval and CallID remain stable across attempts. External effects promise
recoverable at-least-once execution, not exactly-once execution.

Dex retries reuse the first Attribute snapshot. They cannot observe a Lease
generation committed while the tool Step is running. An explicit Lease-expired
result is therefore a known failure. The model may issue one new tool call,
which creates a new Step execution and reads current Lease state.

## Consequences

Timeouts, attempts, and retry exhaustion are visible in the Flow definition.
The MCP registry contains no hidden retry loop. Lease refresh remains proactive;
the model retry path handles rare credential-expiration races without creating
a specialized recovery protocol.
