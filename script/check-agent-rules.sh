#!/bin/sh
# Copyright (c) 2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

if ! cmp -s AGENTS.md CLAUDE.md; then
  echo "AGENTS.md and CLAUDE.md must be identical" >&2
  exit 1
fi

for rule in project-core dex-application go typescript-react openapi testing planning git; do
  test -f ".cursor/rules/$rule.mdc" || {
    echo "missing .cursor/rules/$rule.mdc" >&2
    exit 1
  }
done

test -f skills/dex-developer/SKILL.md
test -f skills/dex-developer/UPSTREAM.md
