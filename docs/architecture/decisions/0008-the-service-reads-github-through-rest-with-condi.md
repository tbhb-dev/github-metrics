# D8. The service reads GitHub through REST with conditional requests and one shared budget

- **Status:** accepted, 2026-11-14.
- **Rule:** the service calls the REST API with `If-None-Match` and sends one request at a time, at least 250 milliseconds after the previous one. One shared token bucket, refilled at 2,500 requests an hour with at most 60 tokens held, serves webhook-triggered reconciles first, then incremental runs, the nightly sweep, and the backfill.
- **Undo:** move the per-PR fetch to one GraphQL query, as appendix B describes.
