# D14. The service records every duration itself, not from spans

- **Status:** accepted, 2026-11-14.
- **Rule:** the service records each timing, size, count, and gauge as a direct OTel instrument, with the commit-then-record rule of D10. Tempo's metrics generator produces none of them. Alloy span metrics are an optional extra and no goal or alert reads them. The generator in Tempo v3.1.0 drops spans that ended more than 30 seconds before ingestion (`MetricsIngestionSlack`, default 30 s, in `modules/generator/config.go`), and this service backdates its spans to GitHub's timestamps. The host runs that version (`o11y/compose.yaml:105` in the ops repository pins `grafana/tempo:3.1.0`). Whether its configuration overrides the default is unverified: the checked-in `o11y/config/tempo/config.yaml` doesn't.
- **Undo:** derive series from spans after raising `MetricsIngestionSlack` past the longest backdating, and accept double counts on a reexport.
