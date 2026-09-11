#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

repository_root=$(git rev-parse --show-toplevel)
consumer_root=$(mktemp -d)
trap 'rm -r "$consumer_root"' EXIT

cp "$repository_root/script/testdata/public-api-consumer/consumer_test.go" "$consumer_root/consumer_test.go"
cd "$consumer_root"

GOWORK=off go mod init example.com/superagent-public-api-consumer >/dev/null
GOWORK=off go mod edit -require github.com/superdurable/superagent@v0.0.0
GOWORK=off go mod edit -replace "github.com/superdurable/superagent=$repository_root"
GOCACHE="$repository_root/.cache/go-build" GOWORK=off go test -mod=mod .
