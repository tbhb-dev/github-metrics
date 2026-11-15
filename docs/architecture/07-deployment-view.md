# 7. Deployment view

The target deployment runs two containers on the production container host: the service and the tunnel connector. This page describes that target. Today `deploy/compose.yaml` mounts only the webhook secret and the tunnel token, and `main` reads only `LISTEN_ADDR` and `WEBHOOK_SECRET_FILE`. The writable volume, the App private key, and the other settings below are design.

| Element | Setting |
| --- | --- |
| Image | Distroless static, non-root, one Go binary, referenced by digest |
| Filesystem | Read-only root, one writable volume at `/var/lib/github-metrics` |
| Network | Off the host network. The tunnel connector makes only outbound connections |
| Secrets | Read-only files. The service gets the webhook secret and, in the original design, the App private key. The connector gets the tunnel token |
| Configuration | Environment variables set the App ID, the installation ID, the listen addresses, the schedule, and the collector endpoint |
| Health | `/healthz` for liveness. `/readyz` is a design item that reports an open store and one finished reconciliation |
| Shutdown | On `SIGTERM`, within 20 seconds the receiver stops accepting requests and the worker finishes its current PR. Then the meter provider flushes and the store closes |
| Delivery | The repository publishes the image and a Compose file. The host operator deploys them |
