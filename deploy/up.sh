#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright Tony Burns
#
# Starts the receiver from an image that names its digest. Compose can't
# check the form of a variable's value, so this script refuses any image
# reference that isn't `<name>@sha256:<64 hex digits>` or `sha256:<64 hex
# digits>`. A name is one or more `/`-separated components. A component is
# runs of lowercase letters and digits joined by `.`, `_`, `__` or dashes, so
# a tag, a registry port, a leading dash, an empty component and `.` or `..`
# are all refused.
#
# The second form is the image id. A host with the classic Docker image store
# re-keys an image on `docker load`, so the digest reference no longer
# resolves and the image runs by its id.
#
# Usage: deploy/up.sh <image> [docker compose up arguments]
set -eu

image=${1:-}

# case sees the whole string, so a newline can't hide a second line from grep.
case $image in
*[[:space:]]*)
  echo "up.sh: the image must not contain whitespace or a newline" >&2
  exit 2
  ;;
esac

if ! printf '%s\n' "$image" | grep -Eq '^([a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*@)?sha256:[0-9a-f]{64}$'; then
  echo "up.sh: the image must be <name>@sha256:<64 hex digits> or sha256:<64 hex digits>, got '$image'" >&2
  exit 2
fi

shift

GITHUB_METRICS_IMAGE=$image exec docker compose \
  -f "$(dirname "$0")/compose.yaml" up -d "$@"
