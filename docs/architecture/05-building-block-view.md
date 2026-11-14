# 5. Building block view

## Level 1: containers

Diagram file: `diagrams/structurizr-Containers.svg`, from the Bazel target.

| Container | Responsibility |
| --- | --- |
| Tunnel connector | Public entry for `POST /webhook`. Outbound connections only |
| Service | The Go binary. Today it has the receiver, and the design adds the worker and the reconciler |
| State store | Planned, with no store in the code yet. One `bbolt` file would hold the delivery queue, the dedupe table, watermarks, entity tags, and the state for exported span IDs and metric milestones |

## Level 2: the service

Diagram file: `diagrams/structurizr-Components.svg`, from the Bazel target.

| Component | Package | Status |
| --- | --- | --- |
| Webhook receiver | `internal/receiver` | Verifies the signature and answers `202`. It stores and processes nothing yet |
| Request budget | `internal/budget` | Built. It ranks callers as webhook, incremental, sweep, then backfill |
| GitHub client | `internal/ghclient` | Built. Conditional requests, paging, token renewal, backoff |
| Worker | planned | Turns reconciled state into spans and observations |
| Reconciler | planned | Incremental, nightly, and backfill walks |
