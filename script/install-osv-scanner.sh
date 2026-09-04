#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

destination=${1:?destination path is required}
version=${2:?OSV Scanner version is required}

case "$(uname -s)" in
  Darwin) operating_system=darwin ;;
  Linux) operating_system=linux ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  arm64|aarch64) architecture=arm64 ;;
  x86_64|amd64) architecture=amd64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset_name="osv-scanner_${operating_system}_${architecture}"
case "$asset_name" in
  osv-scanner_darwin_amd64) checksum=9f89beb6c3d784893cb1cae0a3d56c529bfe91075418c2f9440c45b79654198b ;;
  osv-scanner_darwin_arm64) checksum=75c44d6332f892a1e56286f4105a98ed751ae28d215ca0a8b65cc00d84103054 ;;
  osv-scanner_linux_amd64) checksum=f9f25499a2c8cc367b3af45df2ea7eeca7fbccceab9c35079968f4b3652194be ;;
  osv-scanner_linux_arm64) checksum=3d0f5aa5a6baa8eb32bcef247388e149ef6030a6634ccae6fa0d62681fb27a6d ;;
  *) echo "no checksum is pinned for $asset_name" >&2; exit 1 ;;
esac

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/superagent-osv-scanner.XXXXXX")
trap 'rm -r "$temporary_directory"' EXIT HUP INT TERM
download="$temporary_directory/$asset_name"
url="https://github.com/google/osv-scanner/releases/download/$version/$asset_name"

curl --fail --location --proto '=https' --retry 3 --silent --show-error \
  --tlsv1.2 --output "$download" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum=$(sha256sum "$download" | awk '{print $1}')
else
  actual_checksum=$(shasum -a 256 "$download" | awk '{print $1}')
fi
if [ "$actual_checksum" != "$checksum" ]; then
  echo "OSV Scanner checksum mismatch" >&2
  exit 1
fi

mkdir -p "$(dirname "$destination")"
install -m 0755 "$download" "$destination"
