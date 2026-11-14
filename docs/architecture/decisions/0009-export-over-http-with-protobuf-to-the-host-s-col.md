# D9. Export over HTTP with protobuf to the host's collector

- **Status:** accepted, 2026-11-14.
- **Rule:** the service exports OpenTelemetry Protocol (OTLP) data over HTTP to port 4318 and exports trace spans synchronously per reconcile, without the batch processor. Metrics leave through a periodic reader every 60 seconds. Logs leave over the same endpoint through the OTLP log exporter, with `deployment.environment.name` set, and the collector sends them to Loki. Every service sends its logs to Loki.
- **Undo:** switch the exporter to gRPC on port 4317, which changes the configuration and the dependencies.
