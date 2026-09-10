#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
log_dir=${TEST_LOG_DIR:-/tmp/superagent-full-stack}
http_address=${SUPERAGENT_E2E_HTTP_ADDRESS:-127.0.0.1:8080}
http_origin="http://$http_address"
web_address=${SUPERAGENT_E2E_WEB_ADDRESS:-127.0.0.1:4173}
web_origin="http://$web_address"
worker_address=${SUPERAGENT_E2E_WORKER_ADDRESS:-127.0.0.1:8803}
web_root="$log_dir/web-dist"
mkdir -p "$log_dir"
mkdir -p "$log_dir/mcp-helper"
mkdir -p "$web_root"

cp -R "$repository_dir/web/dist/." "$web_root/"
printf '{\n  "apiOrigin": "%s"\n}\n' "$http_origin" >"$web_root/config.json"

cd "$repository_dir"
GOWORK=off go test -c -o bin/superagent-mcp-fixture.test ./internal/mcp

SUPERAGENT_HTTP_ADDRESS="$http_address" \
SUPERAGENT_HTTP_ALLOWED_ORIGINS="$web_origin" \
DEX_FLOW_SERVICE_ADDRESS=${DEX_FLOW_SERVICE_ADDRESS:-127.0.0.1:8801} \
DEX_WORKER_BIND_ADDRESS="$worker_address" \
DEX_WORKER_TARGET="$worker_address" \
DEX_BLOB_CACHE_DIR="$log_dir/blob-cache" \
DEX_AGENT_MCP_CONFIG="$repository_dir/script/testdata/mcp-fixture.yaml" \
SUPERAGENT_E2E_MCP_HELPER_DIRECTORY="$log_dir/mcp-helper" \
"$repository_dir/bin/superagent" >"$log_dir/backend.log" 2>&1 &
backend_pid=$!

web_host=${web_address%:*}
web_port=${web_address##*:}
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
  if curl --fail --silent "$http_origin/readyz" >/dev/null && \
    curl --fail --silent "$web_origin/config.json" >/dev/null; then
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
SUPERAGENT_E2E_WEB_ORIGIN="$web_origin" \
SUPERAGENT_E2E_API_ORIGIN="$http_origin" \
  npx playwright test --config playwright.full-stack.config.ts
