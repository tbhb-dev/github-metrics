# 2. Constraints

| Constraint | Effect |
| --- | --- |
| Read-only GitHub App | The App has every read-only permission GitHub offers for repositories and the organization, and subscribes to two PR events. It never writes |
| REST only | The service reads GitHub through REST with conditional requests and doesn't use GraphQL |
| Outbound-only network | A Cloudflare Tunnel connector routes only `POST /webhook` to the service, and the host forwards no port |
| Single deployment | Production only, with no staging copy |
| Container image | A distroless static non-root image with one Go binary, a read-only root filesystem, and one writable volume |
| Existing observability stack | The service sends OpenTelemetry data over HTTP to the host collector, which forwards traces, metrics, and logs to the backends |
| No tail sampling | A PR's trace remains incomplete for weeks, and the collector pipeline for this service must not tail-sample |
| Secrets as files | The webhook secret and the tunnel token are files on a read-only mount, never environment variables, and never image content |
