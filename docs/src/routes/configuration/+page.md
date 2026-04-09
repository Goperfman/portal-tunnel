---
title: Configuration Reference
description: Complete reference for all Portal environment variables, CLI flags, and configuration files.
---

# Configuration Reference

Complete reference for all Portal environment variables, CLI flags, and configuration files.

## Relay Server Environment Variables

The relay server (`relay-server`) reads configuration from environment variables. Each variable corresponds to a CLI flag of the same shape (e.g. `PORTAL_URL` → `--portal-url`). CLI flags take precedence over environment variables when both are set.

### Core

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `PORTAL_URL` | `https://localhost:4017` | string | Public base URL of this relay server |
| `API_PORT` | `4017` | int | Admin/API server listen port |
| `SNI_PORT` | `443` | int | TCP SNI router listen port |

### Transport

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `MIN_PORT` | `0` | int | Inclusive minimum port for UDP and raw TCP transports (`0` = disabled) |
| `MAX_PORT` | `0` | int | Inclusive maximum port for UDP and raw TCP transports (`0` = disabled) |
| `UDP_ENABLED` | `false` | bool | Enable UDP relay transport; requires a valid `MIN_PORT`/`MAX_PORT` range |
| `TCP_ENABLED` | `false` | bool | Enable raw TCP port transport; requires a valid `MIN_PORT`/`MAX_PORT` range |

### Features

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `LANDING_PAGE_ENABLED` | `false` | bool | Enable the landing page by default when no admin setting has been saved yet |
| `DISCOVERY` | `false` | bool | Serve relay discovery endpoints and poll discovery peers |
| `BOOTSTRAPS` | `""` | string | Additional bootstrap relay API URLs used for discovery expansion (comma-separated) |

### Identity

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `IDENTITY_PATH` | `identity.json` | string | Relay identity JSON file path |
| `ADMIN_SECRET_KEY` | `""` | string | Admin authentication secret. When empty, the server auto-generates a random key at startup and logs it. |

### Proxy

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `TRUST_PROXY_HEADERS` | `false` | bool | Trust `X-Forwarded-*` and `X-Real-IP` headers from trusted proxies |
| `TRUSTED_PROXY_CIDRS` | `""` | string | Trusted proxy CIDR allowlist for forwarded headers (comma-separated); defaults to private/loopback ranges when `TRUST_PROXY_HEADERS` is enabled |

### TLS

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `KEYLESS_DIR` | `./.portal-certs` | string | Directory path for relay keyless TLS materials |
| `ACME_DNS_PROVIDER` | `""` | string | ACME DNS provider for managed DNS-01/A-record sync and ENS gasless DNSSEC/TXT automation (`cloudflare` \| `gcloud` \| `route53`); leave empty to use manual `fullchain.pem`/`privatekey.pem` from `KEYLESS_DIR` |
| `ENS_GASLESS_ENABLED` | `false` | bool | Enable ENS gasless DNS import automation for the managed DNS zone and lease hostnames |

### Admin

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `ADMIN_SETTINGS_PATH` | `admin_settings.json` | string | Admin settings file path for persisted admin state |
| `HEADLESS_SHELL_URL` | `""` | string | Headless Chrome CDP WebSocket URL for thumbnail generation (e.g. `ws://headless-shell:9222`) |

### Cloudflare

| Variable | Default | Type | Description |
|----------|---------|------|-------------|
| `CLOUDFLARE_TOKEN` | | string | Cloudflare DNS API token; required when `ACME_DNS_PROVIDER=cloudflare` |

### Google Cloud

| Variable | Aliases | Default | Type | Description |
|----------|---------|---------|------|-------------|
| `GCP_PROJECT_ID` | `GOOGLE_CLOUD_PROJECT`, `GCLOUD_PROJECT`, `GCE_PROJECT` | | string | Google Cloud project ID for Cloud DNS automation; auto-detected from ADC or GCE metadata when omitted |
| `GCP_MANAGED_ZONE` | `GCP_ZONE`, `GCE_ZONE_ID` | | string | Explicit Google Cloud DNS managed zone name or numeric ID override |
| `GOOGLE_APPLICATION_CREDENTIALS` | | | string | Path to GCP service account key file (standard ADC; used by the GCP client library) |

### AWS

| Variable | Aliases | Default | Type | Description |
|----------|---------|---------|------|-------------|
| `AWS_ACCESS_KEY_ID` | | | string | AWS access key ID for Route53 static credentials; uses the default AWS credential chain when omitted |
| `AWS_SECRET_ACCESS_KEY` | | | string | AWS secret access key for Route53 static credentials |
| `AWS_SESSION_TOKEN` | | | string | AWS session token for Route53 temporary credentials |
| `AWS_REGION` | `AWS_DEFAULT_REGION` | `us-east-1` | string | AWS region for Route53 and Route53-backed DNS-01 |
| `AWS_HOSTED_ZONE_ID` | | | string | Explicit Route53 hosted zone ID override |
| `AWS_DNSSEC_KMS_KEY_ARN` | | | string | AWS KMS key ARN used to create a Route53 DNSSEC key-signing key when needed |

---

## Portal Tunnel CLI Flags

The `portal expose` subcommand accepts the following flags. Flags that read from environment variables are noted in the **Env Var** column.

### Connection

| Flag | Env Var | Type | Default | Description |
|------|---------|------|---------|-------------|
| `--relays` | | string | _(registry)_ | Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https) |
| `--discovery` | | bool | `true` | Include public registry relays and discover additional relay bootstraps |
| `--ban-mitm` | `BAN_MITM` | bool | `true` | Ban relay when the MITM self-probe detects TLS termination |

### Identity

| Flag | Env Var | Type | Default | Description |
|------|---------|------|---------|-------------|
| `--identity-path` | `IDENTITY_PATH` | string | `identity.json` | Identity JSON file path |
| `--identity-json` | `IDENTITY_JSON` | string | | Identity JSON payload; overrides `--identity-path` contents and is persisted there when both are set |

### Lease

| Flag | Env Var | Type | Default | Description |
|------|---------|------|---------|-------------|
| `--name` | | string | _(auto)_ | Public hostname prefix (single DNS label); auto-generated when omitted |
| `--description` | | string | | Service description metadata |
| `--tags` | | string | | Service tags metadata (comma-separated) |
| `--owner` | | string | | Service owner metadata |
| `--thumbnail` | | string | | Service thumbnail URL metadata |
| `--hide` | | bool | `false` | Hide service from relay listing screens |

### Routing

| Flag | Env Var | Type | Default | Description |
|------|---------|------|---------|-------------|
| `--http-route` | | string | | HTTP route mapping in `PATH=UPSTREAM` form; repeat to aggregate multiple local HTTP services behind one public URL |

### Transport

| Flag | Env Var | Type | Default | Description |
|------|---------|------|---------|-------------|
| `--udp` | `UDP_ENABLED` | bool | `false` | Enable public UDP relay in addition to the default TCP relay |
| `--udp-addr` | `UDP_ADDR` | string | | Local UDP target address for relayed datagrams (`host:port` or port only); defaults to the target when `--udp` is enabled |
| `--tcp` | `TCP_ENABLED` | bool | `false` | Request a dedicated TCP port on the relay for raw TCP services (no TLS; e.g., Minecraft, game servers) |

The `portal list` subcommand accepts the following flags:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--relays` | string | _(registry)_ | Additional Portal relay server API URLs (comma-separated) |
| `--default-relays` | bool | `true` | Include public registry relays |

---

## Configuration Files

### `identity.json`

Stores the cryptographic identity used to sign tunnel sessions. Created automatically by both `relay-server` and `portal expose` on first run.

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Human-readable label for this identity |
| `address` | string | Derived public address (used as the tunnel subdomain seed) |
| `public_key` | string | Ed25519 public key (base64) |
| `private_key` | string | Ed25519 private key (base64); keep secret |

The file path is controlled by `IDENTITY_PATH` / `--identity-path`. The same file can be shared across restarts to keep a stable address.

### `admin_settings.json`

Persists admin-panel state for the relay server. Managed automatically by the relay on write; do not edit manually while the server is running.

The file path is controlled by `ADMIN_SETTINGS_PATH` / `--admin-settings-path`.

---

## ACME DNS Provider Configuration

Set `ACME_DNS_PROVIDER` (or `--acme-dns-provider`) to one of the values below to enable automated TLS certificate issuance via DNS-01 challenges.

When this variable is empty the relay server falls back to manually supplied `fullchain.pem` and `privatekey.pem` files in `KEYLESS_DIR`.

### Cloudflare (`cloudflare`)

| Variable | Required | Description |
|----------|----------|-------------|
| `CLOUDFLARE_TOKEN` | Yes | Cloudflare DNS API token with `Zone:DNS:Edit` permission |

### Google Cloud DNS (`gcloud`)

| Variable | Required | Description |
|----------|----------|-------------|
| `GCP_PROJECT_ID` | No | Google Cloud project ID; auto-detected from ADC or GCE metadata when omitted |
| `GCP_MANAGED_ZONE` | No | Cloud DNS managed zone name or numeric ID; inferred from the portal domain when omitted |
| `GOOGLE_APPLICATION_CREDENTIALS` | No | Path to a service account key JSON file; uses Application Default Credentials when omitted |

### AWS Route53 (`route53`)

| Variable | Required | Description |
|----------|----------|-------------|
| `AWS_ACCESS_KEY_ID` | No | Access key ID; uses the default AWS credential chain (instance profile, env, `~/.aws/credentials`) when omitted |
| `AWS_SECRET_ACCESS_KEY` | No | Secret access key; required when `AWS_ACCESS_KEY_ID` is set |
| `AWS_SESSION_TOKEN` | No | Session token for temporary credentials |
| `AWS_REGION` | No | AWS region; defaults to `us-east-1` |
| `AWS_HOSTED_ZONE_ID` | No | Route53 hosted zone ID; inferred from the portal domain when omitted |
| `AWS_DNSSEC_KMS_KEY_ARN` | No | KMS key ARN for DNSSEC key-signing key creation |
