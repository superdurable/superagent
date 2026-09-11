#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

destination=${1:?destination path is required}
version=${2:?dexcli version is required}

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

archive_name="dexcli_${version}_${operating_system}_${architecture}.tar.gz"
case "$archive_name" in
  dexcli_v0.6.0_darwin_amd64.tar.gz) checksum=7af67aa3b7369df238e3d48e04760994d0acc5ab17e93ccf2d70cd826bca3240 ;;
  dexcli_v0.6.0_darwin_arm64.tar.gz) checksum=1311d384dc15fadc35b9dfa8d64d657fdf44eeb731d75be72af812da91158cfd ;;
  dexcli_v0.6.0_linux_amd64.tar.gz) checksum=7afba1fa375177b8db6d17992ed372eac672bf93320251801e985d300cdff6ee ;;
  dexcli_v0.6.0_linux_arm64.tar.gz) checksum=9304bade4b02f19994b32cca81da57904dbe7bf48a4897981ae5c4689de593ce ;;
  *) echo "no checksum is pinned for $archive_name" >&2; exit 1 ;;
esac

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/superagent-dexcli.XXXXXX")
trap 'rm -r "$temporary_directory"' EXIT HUP INT TERM
archive="$temporary_directory/$archive_name"
url="https://github.com/superdurable/dex/releases/download/cli-$version/$archive_name"

curl --fail --location --proto '=https' --retry 3 --silent --show-error \
  --tlsv1.2 --output "$archive" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum=$(sha256sum "$archive" | awk '{print $1}')
else
  actual_checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
fi
if [ "$actual_checksum" != "$checksum" ]; then
  echo "dexcli checksum mismatch" >&2
  exit 1
fi

tar -xzf "$archive" -C "$temporary_directory"
mkdir -p "$(dirname "$destination")"
install -m 0755 "$temporary_directory/dexcli" "$destination"
