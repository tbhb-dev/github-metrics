# 7. Deployment view

One deployment runs on the production container host with two containers: the service and the tunnel connector.

| Element | Setting |
| --- | --- |
| Image | Distroless static, non-root, one Go binary, referenced by digest |
| Filesystem | Read-only root, one writable volume at `/var/lib/github-metrics` |
| Network | Off the host network. The tunnel connector makes only outbound connections |
| Secrets | Read-only files: the webhook secret and the App private key for the service, and the tunnel token for the connector |
| Configuration | Environment variables set the App ID, the installation ID, the listen addresses, the schedule, and the collector endpoint |
| Health | `/healthz` for liveness. `/readyz` is a design item that reports an open store and one finished reconciliation |
| Shutdown | On `SIGTERM`, within 20 seconds the receiver stops accepting requests and the worker finishes its current PR. Then the meter provider flushes and the store closes |
| Delivery | The repository publishes the image and a Compose file. The host operator deploys them |
