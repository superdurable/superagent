#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

if [ -e reference/python ]; then
  echo "the retired Python parity oracle must not be restored" >&2
  exit 1
fi

operational_references=$(
  git grep -n -F 'reference/python' -- . \
    ':(exclude)MIGRATION.md' \
    ':(exclude)docs/python-go-parity.md' \
    ':(exclude)script/check-cutover.sh' || true
)
if [ -n "$operational_references" ]; then
  printf '%s\n' "operational Python-oracle references remain:" "$operational_references" >&2
  exit 1
fi

grep -q 'd09a5d7b75754d7b8df00bc06df5902507fc5425' docs/python-go-parity.md
