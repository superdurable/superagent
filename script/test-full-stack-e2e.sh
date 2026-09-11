#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
log_dir=${TEST_LOG_DIR:-/tmp/superagent-full-stack}
http_address=${SUPERAGENT_E2E_HTTP_ADDRESS:-127.0.0.1:8080}
api_origin=${SUPERAGENT_E2E_API_ORIGIN:-http://$http_address}
worker_bind_address=${SUPERAGENT_E2E_WORKER_BIND_ADDRESS:-127.0.0.1:8803}
worker_target=${SUPERAGENT_E2E_WORKER_TARGET:-$worker_bind_address}
web_host=${SUPERAGENT_E2E_WEB_HOST:-127.0.0.1}
web_port=${SUPERAGENT_E2E_WEB_PORT:-4173}
web_origin=${SUPERAGENT_E2E_WEB_ORIGIN:-http://$web_host:$web_port}
web_root="$log_dir/web-root"
mkdir -p "$log_dir"
mkdir -p "$log_dir/mcp-helper"
mkdir -p "$web_root"

cd "$repository_dir"
GOWORK=off go test -c -o bin/superagent-mcp-fixture.test ./internal/mcp
cp -R "$repository_dir/web/dist/." "$web_root"
node -e 'require("node:fs").writeFileSync(process.argv[1], JSON.stringify({apiOrigin: process.argv[2]}))' \
  "$web_root/config.json" "$api_origin"

SUPERAGENT_HTTP_ADDRESS="$http_address" \
SUPERAGENT_HTTP_ALLOWED_ORIGINS="$web_origin" \
DEX_FLOW_SERVICE_ADDRESS=${DEX_FLOW_SERVICE_ADDRESS:-127.0.0.1:8801} \
DEX_WORKER_BIND_ADDRESS="$worker_bind_address" \
DEX_WORKER_TARGET="$worker_target" \
DEX_BLOB_CACHE_DIR="$log_dir/blob-cache" \
DEX_AGENT_MCP_CONFIG="$repository_dir/script/testdata/mcp-fixture.yaml" \
SUPERAGENT_E2E_MCP_HELPER_DIRECTORY="$log_dir/mcp-helper" \
"$repository_dir/bin/superagent" >"$log_dir/backend.log" 2>&1 &
backend_pid=$!

python3 -m http.server "$web_port" --bind "$web_host" --directory "$web_root" \
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
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    echo "full-stack backend exited before becoming ready; see $log_dir/backend.log"
    exit 1
  fi
  if ! kill -0 "$web_pid" 2>/dev/null; then
    echo "full-stack web server exited before becoming ready; see $log_dir/web.log"
    exit 1
  fi
  if curl --fail --silent "$api_origin/readyz" >/dev/null && \
    curl --fail --silent "$web_origin/config.json" >/dev/null; then
    # A process that loses a bind race can still be alive briefly while an
    # older listener satisfies the probes. Only accept readiness after both
    # child processes survive one more scheduler turn.
    sleep 0.1
    if kill -0 "$backend_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; then
      ready=true
      break
    fi
  fi
  attempt=$((attempt + 1))
  sleep 0.1
done
if [ "$ready" != true ]; then
  echo "full-stack servers did not become ready"
  exit 1
fi

cd "$repository_dir/web"
SUPERAGENT_E2E_API_ORIGIN="$api_origin" \
SUPERAGENT_E2E_WEB_ORIGIN="$web_origin" \
  npx playwright test --config playwright.full-stack.config.ts
