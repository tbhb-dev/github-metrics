# 3. Context and scope

> Today versus design: the code has a webhook receiver and a GitHub client behind one request budget. The worker, the reconciler, the store, and the export remain design. This page shows the rest as design.

Expected diagram, not yet generated: `diagrams/structurizr-SystemContext.svg`, from `workspace.dsl`.

## Business context

| Partner | Direction | Content |
| --- | --- | --- |
| GitHub | Inbound and outbound | Webhooks for PR and PR review events in. REST reads out remain design, since `main` builds no client |
| Engineers | Outbound only | In the design they read traces, metrics, and logs in the backends. They never call the service |

## Technical context

| Interface | Protocol | Notes |
| --- | --- | --- |
| `POST /webhook` | HTTPS through the tunnel | Signed with `X-Hub-Signature-256`, at most 25 MiB |
| `GET /healthz` | HTTP on the host | Liveness |
| GitHub REST | HTTPS | Planned: bearer installation token, `ETag` conditional requests |
| OpenTelemetry export | HTTP with protobuf | Planned: traces, metrics, and logs to the host collector on port 4318. The code exports nothing yet |

## Scope

In scope: PR, review, review request, commit, reopen, merge, and close activity for every repository the App sees. Out of scope for the first release: checks, issue comments, pushes, review threads, and write access.

The boundary with the organization collector is pending its owner and unverified. [D13](decisions/0013-the-boundary-with-the-organization-collector-pen.md) records it.
