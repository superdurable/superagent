/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  useEffect,
  useMemo,
  useReducer,
  useRef,
  type Dispatch,
  type FormEvent,
} from "react";

import type { Provider } from "./api/generated";
import {
  getPortal,
  startAgent,
  type FlowId,
  type Portal,
  type StartAgentRequest,
  type ToolName,
} from "./api/generated";
import { Conversation } from "./Conversation";

const defaultSystemPrompt =
  "You are a helpful durable AI agent. Use tools when they help and report tool outcomes accurately.";

interface LaunchForm {
  provider: Provider;
  model: string;
  systemPrompt: string;
  maxContextTokens: number;
  messageRetentionLimit: number;
  mcpEnabled: boolean;
  enabledMcpServers: string[];
  enabledTools: ToolName[];
}

type State =
  | { kind: "loading" }
  | {
      kind: "portal";
      portal: Portal;
      form: LaunchForm;
      error: string | null;
      submitting: boolean;
    }
  | { kind: "conversation"; flowId: FlowId; portal: Portal }
  | { kind: "fatal"; message: string };

type Action =
  | { type: "loaded"; portal: Portal; resumedFlowId: FlowId | null }
  | { type: "failed"; message: string }
  | { type: "edit"; update: Partial<LaunchForm> }
  | { type: "starting" }
  | { type: "start-failed"; message: string }
  | { type: "started"; flowId: FlowId };

function initialForm(portal: Portal): LaunchForm {
  const provider =
    portal.providers.find((candidate) => candidate.configured) ??
    portal.providers[0];
  if (provider === undefined)
    throw new Error("The server returned no model providers.");
  return {
    provider: provider.id,
    model: provider.defaultModel,
    systemPrompt: defaultSystemPrompt,
    maxContextTokens: 32_000,
    messageRetentionLimit: 2_000,
    mcpEnabled: true,
    enabledMcpServers: [...portal.mcpServers],
    enabledTools: portal.tools.map((tool) => tool.name),
  };
}

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case "loaded":
      return action.resumedFlowId === null
        ? {
            kind: "portal",
            portal: action.portal,
            form: initialForm(action.portal),
            error: null,
            submitting: false,
          }
        : {
            kind: "conversation",
            flowId: action.resumedFlowId,
            portal: action.portal,
          };
    case "failed":
      return { kind: "fatal", message: action.message };
    case "edit":
      return state.kind === "portal" && !state.submitting
        ? { ...state, form: { ...state.form, ...action.update }, error: null }
        : state;
    case "starting":
      return state.kind === "portal"
        ? { ...state, submitting: true, error: null }
        : state;
    case "start-failed":
      return state.kind === "portal"
        ? { ...state, submitting: false, error: action.message }
        : state;
    case "started":
      return state.kind === "portal"
        ? { kind: "conversation", flowId: action.flowId, portal: state.portal }
        : state;
  }
}

function App() {
  const resumedFlowId = useMemo<FlowId | null>(() => {
    const value = new URLSearchParams(window.location.search).get("flowId");
    return value === null || value.trim() === "" ? null : value;
  }, []);
  const [state, dispatch] = useReducer(reducer, { kind: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    void getPortal({ signal: controller.signal })
      .then((portal) => {
        if (current) dispatch({ type: "loaded", portal, resumedFlowId });
      })
      .catch((reason: unknown) => {
        if (current && !controller.signal.aborted) {
          dispatch({ type: "failed", message: errorMessage(reason) });
        }
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [resumedFlowId]);

  switch (state.kind) {
    case "loading":
      return (
        <Status
          title="Loading Superagent"
          detail="Checking the durable runtime…"
        />
      );
    case "fatal":
      return (
        <Status
          title="Superagent is unavailable"
          detail={state.message}
          danger
        />
      );
    case "conversation":
      return (
        <Conversation
          flowId={state.flowId}
          onStartAnother={() => {
            window.history.replaceState({}, "", window.location.pathname);
            dispatch({
              type: "loaded",
              portal: state.portal,
              resumedFlowId: null,
            });
          }}
        />
      );
    case "portal":
      return <LaunchPortal state={state} dispatch={dispatch} />;
  }
}

export default App;

function LaunchPortal({
  state,
  dispatch,
}: {
  state: Extract<State, { kind: "portal" }>;
  dispatch: Dispatch<Action>;
}) {
  const startController = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      startController.current?.abort();
    },
    [],
  );
  const provider = state.portal.providers.find(
    (candidate) => candidate.id === state.form.provider,
  );
  if (provider === undefined)
    throw new Error("The selected provider is unavailable.");
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (startController.current !== null) return;
    dispatch({ type: "starting" });
    const flowId = crypto.randomUUID();
    const body: StartAgentRequest = {
      flowId,
      provider: state.form.provider,
      model: state.form.model,
      systemPrompt: state.form.systemPrompt,
      maxContextTokens: state.form.maxContextTokens,
      messageRetentionLimit: state.form.messageRetentionLimit,
      mcpEnabled: state.form.mcpEnabled,
      enabledMcpServers: state.form.mcpEnabled
        ? state.form.enabledMcpServers
        : [],
      enabledTools: state.form.mcpEnabled
        ? state.form.enabledTools.filter((name) =>
            state.portal.tools.some(
              (tool) =>
                tool.name === name &&
                (tool.server === null ||
                  state.form.enabledMcpServers.includes(tool.server)),
            ),
          )
        : [],
    };
    const controller = new AbortController();
    startController.current = controller;
    void startAgent({ body, signal: controller.signal })
      .then((response) => {
        if (controller.signal.aborted) return;
        window.history.replaceState({}, "", flowURL(response.flowId));
        dispatch({ type: "started", flowId: response.flowId });
      })
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) {
          dispatch({ type: "start-failed", message: errorMessage(reason) });
        }
      })
      .finally(() => {
        if (startController.current === controller) {
          startController.current = null;
        }
      });
  };

  return (
    <main className="shell">
      <header className="brand">
        <span className="brand-mark" aria-hidden="true">
          S
        </span>
        <div>
          <p>Durable AI runtime</p>
          <h1>Start a Superagent</h1>
        </div>
        <span className="phase-pill">Phase 2</span>
      </header>
      <form className="launch-card" onSubmit={submit}>
        <h2>Model</h2>
        <div className="provider-grid">
          {state.portal.providers.map((item) => (
            <label
              className={item.id === state.form.provider ? "selected" : ""}
              htmlFor={`provider-${item.id}`}
              key={item.id}
            >
              <input
                id={`provider-${item.id}`}
                type="radio"
                name="provider"
                checked={item.id === state.form.provider}
                disabled={!item.configured || state.submitting}
                onChange={() => {
                  dispatch({
                    type: "edit",
                    update: { provider: item.id, model: item.defaultModel },
                  });
                }}
              />
              {item.label}
              <small>
                {item.configured ? item.defaultModel : "Not configured"}
              </small>
            </label>
          ))}
        </div>
        <label>
          Model
          <input
            value={state.form.model}
            maxLength={255}
            required
            disabled={state.submitting}
            onChange={(event) => {
              dispatch({ type: "edit", update: { model: event.target.value } });
            }}
          />
        </label>
        {!provider.configured &&
          provider.credentialEnvironmentVariable !== null && (
            <p className="notice">
              Set <code>{provider.credentialEnvironmentVariable}</code> and
              restart Superagent.
            </p>
          )}

        <h2>Agent behavior</h2>
        <label>
          System prompt
          <textarea
            value={state.form.systemPrompt}
            rows={4}
            maxLength={100_000}
            required
            disabled={state.submitting}
            onChange={(event) => {
              dispatch({
                type: "edit",
                update: { systemPrompt: event.target.value },
              });
            }}
          />
        </label>
        <div className="field-row">
          <NumberInput
            label="Context tokens"
            value={state.form.maxContextTokens}
            maximum={2_000_000}
            disabled={state.submitting}
            onChange={(value) => {
              dispatch({ type: "edit", update: { maxContextTokens: value } });
            }}
          />
          <NumberInput
            label="Retained messages"
            value={state.form.messageRetentionLimit}
            maximum={1_000_000}
            disabled={state.submitting}
            onChange={(value) => {
              dispatch({
                type: "edit",
                update: { messageRetentionLimit: value },
              });
            }}
          />
        </div>

        <h2>MCP tools</h2>
        <label className="switch">
          <input
            type="checkbox"
            checked={state.form.mcpEnabled}
            disabled={state.submitting}
            onChange={(event) => {
              dispatch({
                type: "edit",
                update: { mcpEnabled: event.target.checked },
              });
            }}
          />{" "}
          Enable trusted MCP servers
        </label>
        {state.form.mcpEnabled && (
          <div className="field-row">
            <Choices
              title="Servers"
              choices={state.portal.mcpServers}
              selected={state.form.enabledMcpServers}
              disabled={state.submitting}
              onChange={(enabledMcpServers) => {
                dispatch({ type: "edit", update: { enabledMcpServers } });
              }}
            />
            <Choices
              title="Tools"
              choices={state.portal.tools.map((tool) => tool.name)}
              selected={state.form.enabledTools}
              disabled={state.submitting}
              onChange={(enabledTools) => {
                dispatch({ type: "edit", update: { enabledTools } });
              }}
            />
          </div>
        )}
        <p className="muted">
          Durable built-ins: {state.portal.builtInTools.join(", ")}
        </p>
        {state.error !== null && (
          <p className="error" role="alert">
            {state.error}
          </p>
        )}
        <button
          type="submit"
          disabled={state.submitting || !provider.configured}
        >
          {state.submitting ? "Starting durable Flow…" : "Start agent"}
        </button>
      </form>
    </main>
  );
}

function NumberInput({
  label,
  value,
  maximum,
  disabled,
  onChange,
}: {
  label: string;
  value: number;
  maximum: number;
  disabled: boolean;
  onChange: (value: number) => void;
}) {
  return (
    <label>
      {label}
      <input
        type="number"
        value={value}
        min={1}
        max={maximum}
        required
        disabled={disabled}
        onChange={(event) => {
          onChange(event.target.valueAsNumber);
        }}
      />
    </label>
  );
}

function Choices<T extends string>({
  title,
  choices,
  selected,
  disabled,
  onChange,
}: {
  title: string;
  choices: readonly T[];
  selected: readonly T[];
  disabled: boolean;
  onChange: (selected: T[]) => void;
}) {
  return (
    <fieldset>
      <legend>{title}</legend>
      {choices.length === 0 ? (
        <p className="muted">None configured.</p>
      ) : (
        choices.map((choice) => (
          <label key={choice} className="check-option">
            <input
              type="checkbox"
              checked={selected.includes(choice)}
              disabled={disabled}
              onChange={() => {
                onChange(
                  selected.includes(choice)
                    ? selected.filter((item) => item !== choice)
                    : [...selected, choice],
                );
              }}
            />{" "}
            {choice}
          </label>
        ))
      )}
    </fieldset>
  );
}

function Status({
  title,
  detail,
  danger = false,
}: {
  title: string;
  detail: string;
  danger?: boolean;
}) {
  return (
    <main className="status-shell">
      <section className={danger ? "status-card danger" : "status-card"}>
        <h1>{title}</h1>
        <p>{detail}</p>
      </section>
    </main>
  );
}

function flowURL(flowId: FlowId): string {
  const url = new URL(window.location.href);
  url.search = "";
  url.searchParams.set("flowId", flowId);
  return url.toString();
}

function errorMessage(reason: unknown): string {
  if (reason instanceof Error) return reason.message;
  if (typeof reason === "object" && reason !== null && "detail" in reason) {
    const detail = reason.detail;
    if (typeof detail === "string") return detail;
  }
  return "The request could not be completed.";
}
