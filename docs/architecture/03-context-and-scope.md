# 3. Context and scope

Diagram: `diagrams/structurizr-SystemContext.svg`, generated from `workspace.dsl`.

## Business context

| Partner | Direction | Content |
| --- | --- | --- |
| GitHub | Inbound and outbound | Webhooks for PR and PR review events in, REST reads out |
| Engineers | Outbound only | They read traces, metrics, and logs in the backends. They never call the service |

## Technical context

| Interface | Protocol | Notes |
| --- | --- | --- |
| `POST /webhook` | HTTPS through the tunnel | Signed with `X-Hub-Signature-256`, at most 25 MiB |
| `GET /healthz` | HTTP on the host | Liveness |
| GitHub REST | HTTPS | Bearer installation token, `ETag` conditional requests |
| OpenTelemetry export | HTTP with protobuf | Planned: traces, metrics, and logs to the host collector on port 4318. The code exports nothing yet |

## Scope

In scope: PR, review, review request, commit, reopen, merge, and close activity for every repository the App sees. Out of scope for the first release: checks, issue comments, pushes, review threads, and write access.

The boundary with the organization collector is pending its owner and unverified. These pages leave it out, and the wiki design lists it as D13.
