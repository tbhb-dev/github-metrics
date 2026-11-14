# 4. Solution strategy

| Goal | Approach |
| --- | --- |
| Telemetry doesn't depend on a delivery | A webhook only triggers a reconcile. Telemetry comes from REST responses that show the PR's current state |
| Missed deliveries heal | A 15-minute incremental run and a nightly pass over open PRs reconcile the same way a delivery does |
| Replays and reruns are safe | Trace and span IDs derive from a hash of the PR's node ID, and every run produces the same IDs. The store keeps the exported span IDs |
| Spans have real times | Span times come from GitHub's own timestamps |
| Metrics count once | The worker commits each milestone to the store before it records the observation |
| Bounded request volume | One shared token bucket serves every caller in priority order, and conditional requests make unchanged answers free |
| Bounded memory | The receiver streams each body into the signature hash and never buffers it |
| Logs follow the standard path | The service exports logs to the same collector endpoint as its traces and metrics, and also writes JSON to standard output |
