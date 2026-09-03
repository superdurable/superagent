#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

attempt=1
maximum_attempts=3
while ! npm --prefix web audit --audit-level=moderate \
  --fetch-retries=1 \
  --fetch-retry-mintimeout=1000 \
  --fetch-retry-maxtimeout=5000 \
  --fetch-timeout=30000; do
  if [ "$attempt" -eq "$maximum_attempts" ]; then
    exit 1
  fi
  delay=$((attempt * 5))
  echo "npm audit failed; retrying in ${delay}s" >&2
  sleep "$delay"
  attempt=$((attempt + 1))
done
