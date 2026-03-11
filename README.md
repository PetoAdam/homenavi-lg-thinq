# Homenavi LG ThinQ Integration

LG ThinQ integration for Homenavi using the **official LG ThinQ API (PAT flow)**.

## What this integration uses

- API docs: `openapi.json` (in this repository)
- PAT portal: `https://connect-pat.lgthinq.com`
- Regional ThinQ API base URLs:
  - EMEA: `https://api-eic.lgthinq.com`
  - America: `https://api-aic.lgthinq.com`
  - Asia Pacific: `https://api-kic.lgthinq.com`

## User setup (required data)

1. Open `https://connect-pat.lgthinq.com` and log in with your LG ThinQ account.
2. Create a **Personal Access Token (PAT)**.
3. Copy PAT token.
4. Open integration UI: `http://localhost/integrations/lg-thinq/ui/`.
5. Select region, paste PAT token, keep API key default (from LG docs), then click:
   - `Save setup`
   - `Verify PAT`

## Local test flow

1. Build and run:

```bash
export INTEGRATIONS_ROOT=/path/to/homenavi

docker build -t homenavi-lg-thinq:local .
docker rm -f lg-thinq >/dev/null 2>&1 || true
docker run -d \
	--name lg-thinq \
	--network homenavi-network \
	-p 8099:8099 \
	-e PORT=8099 \
	-e MQTT_BROKER_URL=mqtt://mosquitto:1883 \
	-e JWT_PUBLIC_KEY_PATH=/keys/jwt_public.pem \
	-e LG_THINQ_SETUP_PATH=/app/secrets \
	-v ${INTEGRATIONS_ROOT}/keys/jwt_public.pem:/keys/jwt_public.pem:ro \
	-v ${INTEGRATIONS_ROOT}/integrations/secrets:/app/secrets \
	homenavi-lg-thinq:local
```

Note: `-p 8099:8099` exposes the integration HTTP server on your host. This is convenient for local testing, but it also exposes `POST /api/automation/execute` (which is intended for internal automation execution on the Docker network).

2. Open setup UI:

```text
http://localhost/integrations/lg-thinq/ui/
```

3. In `/ui/`, configure region + PAT token and verify.

## Marketplace install flow

- The integration compose file is `compose/docker-compose.integration.yml`.
- Required runtime base variable:

```bash
export INTEGRATIONS_ROOT=/path/to/homenavi
```

- This compose mounts:
	- `${INTEGRATIONS_ROOT}/integrations/secrets/lg-thinq.setup.json`
	- `${INTEGRATIONS_ROOT}/keys/jwt_public.pem`

After installation via Homenavi admin marketplace, open the installed integration and complete setup in `/ui/`.

## API endpoints

- `GET /healthz`
- `GET /api/admin/setup` (admin)
- `PUT /api/admin/setup` (admin)
- `POST /api/admin/sync-now` (resident)
- `POST /api/admin/auth/login` (admin, PAT verify)
- `POST /api/admin/auth/verify` (admin)
- `POST /api/admin/device-command` (resident)
- `GET /api/realtime/ws` (resident websocket)
- `GET /api/realtime/snapshot` (resident)
- `POST /api/automation/execute` (internal automation execution; no JWT)

## Runtime behavior (default)

- Device updates are primarily event-driven through MQTT/WS paths.
- REST sync is not triggered automatically by realtime events or device commands.
- Backend cloud sync runs periodically using `sync_interval_sec` (default: 180 seconds).
- `POST /api/admin/sync-now` remains available as an on-demand fallback.

## Security

- JWT auth via `JWT_PUBLIC_KEY_PATH`.
- CSP/headers hardened; no inline JS required.
- Setup persistence uses restricted file mode (`0600`).

## Development checks

```bash
go test ./...
go build ./...
go run ./cmd/validate-manifest
```
