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
# The script uses only grep and dirname besides the shell and docker.
#
# Nothing but `--pull never` may follow the image, so a caller can't change
# the Compose file, the project or the environment through arguments after the
# check. The environment still reaches Compose: COMPOSE_PROJECT_NAME,
# COMPOSE_ENV_FILES, COMPOSE_PROFILES, DOCKER_HOST, DOCKER_CONTEXT and
# DOCKER_CONFIG all keep their effect, so the caller must control them. A
# loaded image has no registry to pull from, so an image id needs
# `--pull never`.
#
# Usage: deploy/up.sh <image> [--pull never]
set -eu

# The character classes below mean ASCII only, whatever the caller's locale.
LC_ALL=C
export LC_ALL

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

refuse_args() {
  echo "up.sh: the only argument allowed after the image is '--pull never'" >&2
  exit 2
}

case $# in
0) ;;
2) if [ "$1" != --pull ] || [ "$2" != never ]; then refuse_args; fi ;;
*) refuse_args ;;
esac

GITHUB_METRICS_IMAGE=$image exec docker compose \
  -f "$(dirname "$0")/compose.yaml" up -d "$@"
