# Agent instructions

## What this is

`github-metrics` is a planned Go service that turns GitHub pull request activity into OpenTelemetry traces and metrics. The original design is the `drafts/github-metrics` folder in the public `tbhb-dev/wiki` repository at commit `8e06fb1`. The Go module path is `github.com/tbhb-dev/github-metrics`.

This repository is public. Never write a private repository name, a host name, an internal path, private network details, a public IP address, or a secret in it.

## Layout

`internal/` holds the Go packages. `tools/` holds the lint package and the workspace status script that stamps the version. `.github/` holds the workflows, the CODEOWNERS file, and the ruleset.

## Commands

Bazel pins every tool and runs every check. The only things [mise](https://mise.jdx.dev) installs are `bazelisk` and `prek`, at the versions `mise.toml` and `mise.lock` pin.

| Command | Purpose |
| --- | --- |
| `bazelisk test //...` | Run every check |
| `bazelisk query 'tests(//...)'` | List the checks |
| `bazelisk run //:format` | Format the tree in place |
| `bazelisk run //:buildifier` | Format the Bazel files |
| `bazelisk run //:gazelle` | Regenerate the `BUILD.bazel` files from the Go sources |
| `bazelisk mod tidy` | Update `use_repo` in `MODULE.bazel` |

Each check runs by its target, such as `bazelisk test //:vale_test`.

## Conventions

Change only the files the task needs, and stage paths by name. Run `bazelisk test //...` before committing. Write each Markdown paragraph on one line. `rumdl`, Vale and cspell check the Markdown and the Go comments, so keep the prose plain.

Commit messages follow Conventional Commits. Open and merge pull requests with `tbhbdev alpha pr` and `tbhbdev alpha merge`. Push only to a branch, and never to `main`.
