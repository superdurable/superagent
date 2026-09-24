# ADR 0017: Optional model hooks and exact per-call usage

## Status

Accepted for SuperAgent `v0.7.0`.

## Context

Embedding applications may need to admit model calls against account policy and
synchronize exact provider usage. That policy is application-owned. Putting
accounts, projects, quotas, totals, or recovery state in SuperAgent would couple
the reusable Agent Flow to one control plane.

The model provider response is an external effect. Retrying accounting after a
successful provider call must not repeat that call, and retrying Agent state
application must not repeat accounting.

## Decision

`ModelClient.Complete` and `Summarize` return `ModelUsage` separately from their
existing result. `ModelReply` remains unchanged. Every successful legacy or
hooked Call Step overwrites one `ModelUsage` Attribute; it never accumulates
usage.

`WithModelHooks` is optional and requires both a Before and After hook. Hooked
calls execute as four Dex Steps:

1. `BeforeModelCall` creates a stable call ID and performs admission.
2. `CallModelWithHooks` calls the provider and writes `ModelUsage`.
3. `AfterModelCall` reads that Attribute and synchronizes usage idempotently.
4. `ApplyModelResult` applies the persisted provider result.

The call ID derives from Flow ID, the Before Step execution ID, and call kind.
It is carried in Step input, not an Attribute. OpenAI receives it as its
idempotency key. Hooked calls permit only OpenAI and Mock until other adapters
report exact usage.

OpenAI response storage is an independent client setting. Complete and
Summarize always send `store`; the default is true and
`DisableModelResponseStore` changes it to false.

## Consequences

After retries cannot repeat the provider call, and Apply retries cannot repeat
the After hook. The embedding application owns account lookup, monthly totals,
limits, and idempotency for synchronized usage.

SuperAgent has no usage reporter, accumulated usage Attribute, model checkpoint
state, pending recovery record, model recovery API, or recovery UI.
