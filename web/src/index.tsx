/*
 * Copyright (c) 2022-2026 Super Durable, Inc.
 * Licensed under the Apache License, Version 2.0.
 * SPDX-License-Identifier: Apache-2.0
 */

import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { client } from "./api/generated/client.gen";
import "./app.css";
import { loadRuntimeConfig } from "./runtime-config";

const rootElement = document.getElementById("root");
if (rootElement === null) throw new Error("Missing root element.");
const root = ReactDOM.createRoot(rootElement);

void bootstrap();

async function bootstrap(): Promise<void> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => {
    controller.abort();
  }, 10_000);
  try {
    const config = await loadRuntimeConfig(controller.signal);
    client.setConfig({ baseUrl: config.apiOrigin.value });
    root.render(
      <React.StrictMode>
        <App />
      </React.StrictMode>,
    );
  } catch (reason: unknown) {
    root.render(
      <React.StrictMode>
        <main className="shell status danger" role="alert">
          <h1>Superagent configuration failed</h1>
          <p>{errorMessage(reason)}</p>
        </main>
      </React.StrictMode>,
    );
  } finally {
    window.clearTimeout(timeout);
  }
}

function errorMessage(reason: unknown): string {
  if (reason instanceof DOMException && reason.name === "AbortError") {
    return "Loading config.json timed out.";
  }
  return reason instanceof Error
    ? reason.message
    : "Loading config.json failed.";
}
