# 6. Runtime view

## Scenario: a webhook delivery

1. GitHub posts a delivery through the tunnel to the receiver.
2. The receiver checks the method, the media type, and the size first. It then streams the body into the `HMAC` and compares the result with `X-Hub-Signature-256`.
3. After the signature passes, one store transaction checks the dedupe table and records the delivery. In the same transaction, the receiver adds the body to the queue. A file sync follows, then the answer `202`.
4. Using the signed body, the worker finds the repository and PR number and marks the PR. Afterward it deletes the body.
5. Next, the worker fetches the current PR state with conditional requests and compares it with what the service already exported.
6. Last, the worker exports spans that just completed. The store records their IDs only after the collector accepts them.

Today the receiver stops after the signature check. The design still has to deliver steps 3 to 6.

## Scenario: scheduled reconciliation

Every 15 minutes the reconciler lists each repository's PRs by update time with `If-None-Match` and walks back to the repository's watermark. Once a night it also reconciles every open PR. A first install triggers a backfill that produces traces and no metrics.

## Scenario: a failure

| Failure | Behavior |
| --- | --- |
| Bad signature | `401`, no store access |
| Body over 25 MiB | `413` |
| Store error or full disk | `503`. GitHub records a failed delivery and reconciliation heals it |
| Collector down | The failed export leaves the store unchanged and the PR retries on the next pass |
| Secondary rate limit | The client backs the budget off up to 15 minutes and retries |
