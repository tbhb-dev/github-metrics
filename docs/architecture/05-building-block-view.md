# 5. Building block view

## Level 1: containers

Expected diagram, not yet generated: `diagrams/structurizr-Containers.svg`, from the Bazel target.

| Container | Responsibility |
| --- | --- |
| Tunnel connector | Public entry for `POST /webhook`. Outbound connections only |
| Service | The Go binary. Today it has the receiver, and the design adds the worker and the reconciler |
| State store | Planned, with no store in the code yet. One `bbolt` file would hold the delivery queue, the dedupe table, watermarks, entity tags, and the state for exported span IDs and metric milestones, and a table of open PRs for the gauges |

## Level 2: the service

Expected diagram, not yet generated: `diagrams/structurizr-Components.svg`, from the Bazel target.

| Component | Package | Status |
| --- | --- | --- |
| Webhook receiver | `internal/receiver` | Verifies the signature, checks the delivery and event headers, and answers `202` or `400`. It stores and processes nothing yet |
| Request budget | `internal/budget` | Built. It ranks callers as webhook, incremental, sweep, then backfill |
| GitHub client | `internal/ghclient` | Built. Conditional requests, paging, token renewal, backoff |
| Worker | planned | Turns reconciled state into spans and observations |
| Reconciler | planned | Incremental, nightly, and backfill walks |
