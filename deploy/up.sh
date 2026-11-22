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
# The host needs grep, dirname, mktemp and rm besides the shell and docker. A pinned
# BusyBox container prepares the key through the same Docker access as Compose.
#
# Nothing but `--pull never` may follow the image, so a caller can't change
# the Compose file, the project or the environment through arguments after the
# check. Without sudo, COMPOSE_PROJECT_NAME, COMPOSE_PROFILES, DOCKER_HOST,
# DOCKER_CONTEXT and DOCKER_CONFIG keep their effect, so the caller must
# control them. The script reads .env beside compose.yaml. A
# loaded image has no registry to pull from, so an image id needs
# `--pull never`.
#
# Usage: [DEPLOY_DOCKER_SUDO=1] deploy/up.sh <image> [--pull never]
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

deploy_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)

docker_cmd() {
  if [ "${DEPLOY_DOCKER_SUDO:-0}" = 1 ]; then
    sudo docker "$@"
  else
    docker "$@"
  fi
}

if [ ! -f "$deploy_dir/secrets/github_app_key" ] || [ -L "$deploy_dir/secrets/github_app_key" ]; then
  echo "up.sh: secrets/github_app_key must exist as a regular file, not a symlink" >&2
  exit 2
fi

# File secrets keep host ownership. Only Docker needs root access here.
# The root-owned directory blocks host readers before the copy changes owner.
# A rename publishes the complete copy; recreation replaces old bind mounts.
# The helper shell, rather than the host shell, expands its variables.
# shellcheck disable=SC2016
docker_cmd run --rm --network none --read-only --log-driver none \
  --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
  --security-opt no-new-privileges:true \
  --mount "type=bind,src=$deploy_dir/secrets/github_app_key,dst=/source,readonly" \
  --mount "type=bind,src=$deploy_dir/secrets,dst=/secrets" \
  busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec \
  sh -ec '
    case $(stat -c %a /source) in
      400|600) ;;
      *) echo "up.sh: secrets/github_app_key must have mode 400 or 600" >&2; exit 2 ;;
    esac
    if [ -L /secrets/runtime ] || { [ -e /secrets/runtime ] && [ ! -d /secrets/runtime ]; }; then
      echo "up.sh: secrets/runtime must be a directory, not a symlink" >&2
      exit 2
    fi
    umask 077
    mkdir -p -m 700 /secrets/runtime
    chown 0:0 /secrets/runtime
    chmod 700 /secrets/runtime
    key_tmp=$(mktemp /secrets/runtime/github_app_key.XXXXXX)
    trap '\''rm -f "$key_tmp"'\'' 0
    cp /source "$key_tmp"
    chmod 400 "$key_tmp"
    chown 65532:65532 "$key_tmp"
    mv -fT "$key_tmp" /secrets/runtime/github_app_key
  '

# This temporary env file carries only the checked image reference. It also
# works when sudo strips GITHUB_METRICS_IMAGE from the environment. Compose
# reads env files more than once, so a pipe through /dev/stdin won't work.
image_env=$(mktemp "$deploy_dir/secrets/.image.XXXXXX")
trap 'rm -f "$image_env"' 0
trap 'exit 1' 1 2 15
printf 'GITHUB_METRICS_IMAGE=%s\n' "$image" >"$image_env"
set -- --env-file "$image_env" -f "$deploy_dir/compose.yaml" up -d --force-recreate "$@"
if [ -f "$deploy_dir/.env" ]; then
  set -- --env-file "$deploy_dir/.env" "$@"
fi
GITHUB_METRICS_IMAGE=$image docker_cmd compose "$@"
