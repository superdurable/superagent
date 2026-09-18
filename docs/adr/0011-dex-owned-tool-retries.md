# ADR 0011: Let Dex own external tool retries

## Status

Accepted on 2026-09-11.

## Context

The MCP registry previously maintained its own attempt loop, deadlines,
backoff, and sleep. Those retries were invisible to the durable Flow graph and
could not recover correctly across Worker loss.

## Decision

`ToolDefinition` policy is applied to `ExecuteTool` through movement
StepOptions. Each registry call performs one external attempt. Known business
failures return a normal error result. Transient or ambiguous failures return a
Go error and use Dex retry. Exhaustion follows the tool definition's recovery
policy. It defaults to the manual boundary introduced by ADR 0013; explicitly
configured tools may route to `RecoverToolExecution` and continue with unknown.

Tool execution Steps have no registered StepOptions. The serial safe-boundary
router and each parallel movement resolve the selected `ToolDefinition` and
attach complete options with `WithStepOptions`. Manual retries re-resolve the
definition instead of reusing process-local policy.

New Agent Flows default Step durability to ASYNC. Short-running tools inherit
that default and may fall back to regular execution. Long-running tools override
Execute durability to SYNC. Every regular attempt uses a one-minute heartbeat
timeout. Attempt timeout bounds both Dex execution and the registry child
context.

Approval and CallID remain stable across attempts. External effects promise
recoverable at-least-once execution, not exactly-once execution.

## Consequences

Timeouts, attempts, and retry exhaustion are visible in the Flow definition.
The MCP registry contains no hidden retry loop. Runtime metadata and stable
Flow and call IDs remain unchanged across retries.
