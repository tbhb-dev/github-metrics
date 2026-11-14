# 5. Building block view

## Level 1: containers

Diagram file: `diagrams/structurizr-Containers.svg`, from the Bazel target.

| Container | Responsibility |
| --- | --- |
| Tunnel connector | Public entry for `POST /webhook`. Outbound connections only |
| Service | The Go binary with the receiver, the worker, and the reconciler |
| State store | One `bbolt` file with the delivery queue, the dedupe table, exported span IDs, metric milestones, watermarks, and entity tags |

## Level 2: the service

Diagram file: `diagrams/structurizr-Components.svg`, from the Bazel target.

| Component | Package | Status |
| --- | --- | --- |
| Webhook receiver | `internal/receiver` | Verifies the signature and answers `202`. It stores and processes nothing yet |
| Request budget | `internal/budget` | Built. It ranks callers as webhook, incremental, sweep, then backfill |
| GitHub client | `internal/ghclient` | Built. Conditional requests, paging, token renewal, backoff |
| Worker | planned | Turns reconciled state into spans and observations |
| Reconciler | planned | Incremental, nightly, and backfill walks |
