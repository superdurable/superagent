#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
log_dir=${TEST_LOG_DIR:-/tmp/superagent-full-stack}
mkdir -p "$log_dir"
mkdir -p "$log_dir/mcp-helper"

cd "$repository_dir"
GOWORK=off go test -c -o bin/superagent-mcp-fixture.test ./internal/mcp

SUPERAGENT_HTTP_ADDRESS=127.0.0.1:8080 \
SUPERAGENT_HTTP_ALLOWED_ORIGINS=http://127.0.0.1:4173 \
DEX_FLOW_SERVICE_ADDRESS=${DEX_FLOW_SERVICE_ADDRESS:-127.0.0.1:8801} \
DEX_WORKER_BIND_ADDRESS=127.0.0.1:8803 \
DEX_WORKER_TARGET=127.0.0.1:8803 \
DEX_BLOB_CACHE_DIR="$log_dir/blob-cache" \
DEX_AGENT_MCP_CONFIG="$repository_dir/script/testdata/mcp-fixture.yaml" \
SUPERAGENT_E2E_MCP_HELPER_DIRECTORY="$log_dir/mcp-helper" \
"$repository_dir/bin/superagent" >"$log_dir/backend.log" 2>&1 &
backend_pid=$!

python3 -m http.server 4173 --bind 127.0.0.1 --directory "$repository_dir/web/dist" \
  >"$log_dir/web.log" 2>&1 &
web_pid=$!

cleanup() {
  kill "$web_pid" "$backend_pid" 2>/dev/null || true
  wait "$web_pid" 2>/dev/null || true
  wait "$backend_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

ready=false
attempt=0
while [ "$attempt" -lt 200 ]; do
  if curl --fail --silent http://127.0.0.1:8080/readyz >/dev/null && \
    curl --fail --silent http://127.0.0.1:4173/config.json >/dev/null; then
    ready=true
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.1
done
if [ "$ready" != true ]; then
  echo "full-stack servers did not become ready"
  exit 1
fi

cd "$repository_dir/web"
npx playwright test --config playwright.full-stack.config.ts
