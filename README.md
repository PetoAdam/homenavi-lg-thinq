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
	-e MQTT_BROKER_URL=mqtt://mqtt-broker:1883 \
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


The integration exposes scalar readings from appliance states, including nested and array entries. Washer and combined laundry commands require a matching writable profile and location; starting also requires the reported remote-control setting. Supported enum controls for air conditioners, dishwashers, purifiers, dryers, stylers, boilers, humidifiers, dehumidifiers, fans, ventilators, robot cleaners and water heaters are derived from writable values in the device profile. Refrigerators and wine cellars can expose profile-validated compartment temperature controls when the matching location and unit are reported. Refrigerators can also expose power saving when the profile permits both boolean values and the current value is known. Hoods and microwaves can expose paired lamp and ventilation controls when both ranges and current values are available. Legacy TV controls are also available.

This is not complete command support for every LG device. Other device-specific commands remain unsupported; devices without a recognized safe control are read-only. Paired lamp and ventilation settings are submitted together with Apply. A change from another controller may not yet be reflected in the displayed state; the LG API does not provide a compare-and-swap guarantee. Availability depends on the model, state and LG API permissions. Tests use LG's published examples and mocks, not physical devices. See [the device support plan](device-support-plan.md) for remaining work.

- JWT auth via `JWT_PUBLIC_KEY_PATH`.
- CSP/headers hardened; no inline JS required.
- `LG_THINQ_REALTIME_ENABLED` provides the default only when no realtime setting is saved. An explicit saved setting takes precedence.
- Configure a unique, stable client ID for each installation, as recommended by the ThinQ Connect SDK; changing an existing ID can require re-registering realtime credentials.

When PAT verification fails, the setup UI shows the LG error code and a safe remediation message. In particular, LG code `1314` means the account API-call quota is exhausted; wait for LG's quota window to reset and reduce or disable realtime retries before trying again. The UI distinguishes this from a Homenavi session-expiry error, unsupported country, and PAT permission failures.

## Development checks

```bash
go test ./...
go build ./...
go run ./cmd/validate-manifest
```

The Go suite includes HTTP mock contract tests derived from [thinqconnect 1.0.14](https://github.com/thinq-connect/pythinqconnect/tree/1.0.14), including discovery, profile and state responses, UUIDv4 message IDs, conditional-control requests and error envelopes. The device fixtures come from the bundled LG OpenAPI examples; these tests do not execute the Python SDK or contact LG devices.
