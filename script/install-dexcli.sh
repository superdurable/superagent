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
  dexcli_v0.1.21_darwin_amd64.tar.gz) checksum=c907c30862d00e8fc1df73f4eb4523a9e65ff6a4cef6feb8756ee5c89405a4a1 ;;
  dexcli_v0.1.21_darwin_arm64.tar.gz) checksum=8d5450184dbb7b10ff699a045bac0478e95214dad0caf1fdaa51cdf5af7d79c1 ;;
  dexcli_v0.1.21_linux_amd64.tar.gz) checksum=1a2b554fc4ee459ad63dcd5806875022cb7fec193f62fb17ccb657306995a77f ;;
  dexcli_v0.1.21_linux_arm64.tar.gz) checksum=4ecafb61bdead256c69fcebc73890706aea95d0d257a4eaceb2de4f62968a637 ;;
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
