# D12. Telemetry contains no PR titles, bodies, comment text, or diffs

- **Status:** accepted, 2026-11-14.
- **Rule:** spans record repository and base branch names, PR numbers, logins, team slugs, states, and times, for private repositories too. The worker marks a raw webhook body, which contains titles and review text, deleted once it extracts the repository and PR, or after 7 days in the dead-letter bucket. This is logical deletion: `bbolt` keeps the bytes in the file until it reuses the pages (page 11). The span and metric rule describes the design. Logs meet it only after header validation or redaction exists, because the receiver writes `X-GitHub-Delivery` and `X-GitHub-Event` to the log verbatim today (page 8). The operator sets backend access and retention for this telemetry, and the threat-model follow-up must confirm them.
- **Undo:** add a title attribute in a later release.
