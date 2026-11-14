# 8. Cross-cutting concepts

## Identity

Trace IDs are the first 16 bytes of a SHA-256 digest over a fixed prefix and the PR's GraphQL node ID. Span IDs are the first 8 bytes of a digest that also covers the span kind and a key unique within the PR. Webhook and reconciliation runs derive the same IDs.

## Request budget

One token bucket serves all callers, refilled at 2,500 requests an hour with a burst of 60 and 250 ms between requests. Callers wait by priority, from webhook to backfill. The bucket stops at a floor of 500 remaining requests until the limit resets. Steady state uses about 9% of GitHub's 5,000 request floor.

## Rate-limit handling

A `429`, or a `403` that cites a secondary limit, backs the budget off from one minute, doubling to 15 minutes. A `401` renews the installation token once. A redirect, a body over 16 MiB, or a target on another host is an error.

## Security

Only a request with a valid signature reads or writes state. Tokens come from a `TokenSource` interface, and `main` has only that interface. The wiki design has the service sign the installation JSON Web Token with the App private key, mounted as a read-only file. The operator may instead choose a separate minter, which would keep the key out of the service process. That choice is open. Secrets are files.

## Telemetry content

Spans hold repository and branch names, PR numbers, logins, team slugs, states, and times. Metrics record only the repository name and the author type. Titles, bodies, comment text, and diffs never appear.

## Logging

The service uses `slog` with a JSON handler on standard output and a second handler that exports each record to the collector as an `OTLP` log. The export sets `deployment.environment.name`, which Loki indexes as a label. A log line records the delivery ID, the event, and the result. It never includes a body, a header value, or a secret.

## Testing

Budget tests use `testing/synctest` with a fake clock. The client tests use a fake `RoundTripper`, because `synctest` can't advance time while a goroutine waits on a socket.
