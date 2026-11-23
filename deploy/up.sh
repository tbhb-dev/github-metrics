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
# The host needs awk, grep, dirname, mktemp and rm besides the shell and docker.
# A pinned BusyBox container prepares the key through the same Docker access as Compose.
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
dot_env=$deploy_dir/.env

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

# An exported value takes precedence over .env, as it does in Compose.
# Read a literal assignment without running .env as a shell script.
# cspell:ignore gsub
resolver=${RECEIVER_RESOLV_CONF-}
if [ "${RECEIVER_RESOLV_CONF+x}" != x ] && [ -f "$dot_env" ]; then
  resolver=$(awk '
    function closing_quote(line, quote, start,   i, char) {
      for (i = start; i <= length(line); i++) {
        char = substr(line, i, 1)
        if (char == "\\") i++
        else if (char == quote) return i
      }
      return 0
    }
    multiline {
      if (closing_quote($0, multiline, 1)) multiline = ""
      next
    }
    /^[[:blank:]]*(export[[:blank:]]+)?[^#=:[:space:]]+[[:blank:]]*[=:]/ {
      key = $0
      sub(/^[[:blank:]]*(export[[:blank:]]+)?/, "", key)
      sub(/[[:blank:]]*[=:].*$/, "", key)
      sub(/^[^=:]*[=:][[:blank:]]*/, "")
      quote = substr($0, 1, 1)
      if (quote == "\047" || quote == "\"") {
        end = closing_quote($0, quote, 2)
        if (!end) multiline = quote
        rest = substr($0, end + 1)
        value = end && rest ~ /^[[:space:]]*(#.*)?$/ ? substr($0, 2, end - 2) : ""
      } else {
        sub(/ #.*/, "")
        sub(/[[:space:]]+$/, "")
        value = $0
      }
      if (key != "RECEIVER_RESOLV_CONF") next
      if (quote == "\047") gsub(/\\\047/, "\047", value)
      else if (value ~ /\$/ || (quote == "\"" && value ~ /\\/)) value = ""
      resolver = value
    }
    END { print resolver }
  ' "$dot_env" 2>/dev/null) || resolver=
fi

# Compose resolves relative bind sources from the directory of compose.yaml.
case $resolver in
'' | /*) ;;
*) resolver=$deploy_dir/$resolver ;;
esac
if [ ! -f "$resolver" ] || [ ! -s "$resolver" ]; then
  echo "up.sh: RECEIVER_RESOLV_CONF must name a non-empty regular file" >&2
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

# The final env file carries the checked inputs even when sudo strips them
# from the environment. Escape the path for a double-quoted Compose value.
resolver_env=$(printf '%s\n' "$resolver" | awk '
  NR > 1 { printf "\\n" }
  {
    for (i = 1; i <= length($0); i++) {
      char = substr($0, i, 1)
      if (char == "\\" || char == "\"" || char == "$") printf "\\"
      printf "%s", char
    }
  }
')
image_env=$(mktemp "$deploy_dir/secrets/.image.XXXXXX")
trap 'rm -f "$image_env"' 0
trap 'exit 1' 1 2 15
printf 'GITHUB_METRICS_IMAGE=%s\nRECEIVER_RESOLV_CONF="%s"\n' "$image" "$resolver_env" >"$image_env"
set -- --env-file "$image_env" -f "$deploy_dir/compose.yaml" up -d --force-recreate "$@"
if [ -f "$dot_env" ]; then
  set -- --env-file "$dot_env" "$@"
fi
GITHUB_METRICS_IMAGE=$image RECEIVER_RESOLV_CONF=$resolver docker_cmd compose "$@"
