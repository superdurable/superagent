#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

repository_root=$(git rev-parse --show-toplevel)
scratch_directory=$(mktemp -d "${TMPDIR:-/tmp}/superagent-generated.XXXXXX")
trap 'rm -r "$scratch_directory"' EXIT HUP INT TERM

cp -R "$repository_root/internal/api/generated" "$scratch_directory/go"
cp -R "$repository_root/web/src/api/generated" "$scratch_directory/typescript"

GOCACHE="$repository_root/.cache/go-build" GOWORK=off \
  go generate "$repository_root/internal/api"
npm --prefix "$repository_root/web" run generate:api >/dev/null

diff -ru "$scratch_directory/go" "$repository_root/internal/api/generated"
diff -ru "$scratch_directory/typescript" "$repository_root/web/src/api/generated"
