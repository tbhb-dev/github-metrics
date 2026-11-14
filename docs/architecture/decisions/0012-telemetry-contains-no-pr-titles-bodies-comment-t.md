# D12. Telemetry contains no PR titles, bodies, comment text, or diffs

- **Status:** accepted, 2026-11-14.
- **Rule:** spans record repository and base branch names, PR numbers, logins, team slugs, states, and times, for private repositories too. Raw webhook bodies, which contain titles and review text, stay in the store only until the worker extracts the repository and PR, or 7 days in the dead-letter bucket. The operator sets backend access and retention for this telemetry, and the threat-model follow-up must confirm them.
- **Undo:** add a title attribute in a later release.
