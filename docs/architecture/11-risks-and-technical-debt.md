# 11. Risks and technical debt

| Risk or debt | Effect | Handling |
| --- | --- | --- |
| The backend hides spans dated weeks in the past | Old PR traces don't appear in search | The first export spike sends a backdated trace and searches for it |
| Trace retention is shorter than a PR's life | Early child spans age out before the root arrives | Set retention past the longest expected PR |
| The OTel Go SDK can't set chosen span IDs | The trace model needs another export path | A custom ID generator, with direct export to the collector as the fallback |
| The collector's log path differs from the plan | Logs don't reach Loki | The first export spike sends a log record and searches for it, with standard output as the fallback |
| A body of up to 25 MiB reaches the queue | Disk and queue size grow | Planned: spool to a bounded temporary file and verify the signature before the append, with a proposed cap of four bodies in flight |
| Deleted bodies remain in the `bbolt` file | Raw bodies persist until `bbolt` reuses the pages | Proposed: mode `0600` and a dedicated volume. Whether to back up the queue is open, and plan step 6 decides it |
| The client stores an `ETag` on receipt | A 200 body the reconciler can't use leaves a stale tag | The planned reconciler clears the tag in that case |
