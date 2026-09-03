#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

repository_root=$(git rev-parse --show-toplevel)
unformatted_go=$(gofmt -l "$repository_root/cmd" "$repository_root/internal")
if [ -n "$unformatted_go" ]; then
  printf '%s\n' "Go files require gofmt:" "$unformatted_go" >&2
  exit 1
fi

npm --prefix "$repository_root/web" run format:check
