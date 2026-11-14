#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright Tony Burns
#
# Starts the receiver from an image that names its digest. Compose can't
# check the form of a variable's value, so this script refuses any image
# reference that isn't `<name>@sha256:<64 hex digits>`, such as a tag.
#
# Usage: deploy/up.sh <image> [docker compose up arguments]
set -eu

image=${1:-}

if ! printf '%s\n' "$image" | grep -Eq '^[^@[:space:]]+@sha256:[0-9a-f]{64}$'; then
  echo "up.sh: the image must be <name>@sha256:<64 hex digits>, got '$image'" >&2
  exit 2
fi

shift

GITHUB_METRICS_IMAGE=$image exec docker compose \
  -f "$(dirname "$0")/compose.yaml" up -d "$@"
