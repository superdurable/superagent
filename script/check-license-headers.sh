#!/bin/sh
# Copyright (c) 2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

git ls-files --cached --others --exclude-standard | while IFS= read -r path; do
  test -f "$path" || continue
  case "$path" in
    reference/python/*|skills/dex-developer/*|internal/webui/assets/*|*/gen/*|*/generated/*|*.gen.*|*.generated.*)
      continue
      ;;
    *.go|*.ts|*.tsx|*.js|*.jsx|*.css|*.html|*.sh|*.py|api/*.yaml)
      if ! head -n 24 "$path" | grep -q 'SPDX-License-Identifier: Apache-2.0'; then
        echo "missing Apache-2.0 header: $path" >&2
        exit 1
      fi
      ;;
  esac
done
