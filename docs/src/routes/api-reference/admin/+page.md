---
title: Admin API
description: Portal relay admin endpoints for auth, state, settings, and access control.
---

# Admin API

Admin endpoints are the operator control surface for a relay. They all return
the standard JSON envelope described in [API Reference](/api-reference), except
for internal operational endpoints that are not part of the stable API.

`/admin` is reserved for the frontend route. The relay API begins under the
specific paths listed below.

## Auth Flow

1. `POST /admin/auth/challenge` with the wallet address.
2. Sign the returned `siwe_message`.
3. `POST /admin/auth/login` with the challenge id, message, and signature.
4. Send the returned `access_token` as `Authorization: Bearer <token>`.
5. `POST /admin/auth/logout` to invalidate the current token.

Admin bearer tokens are separate from SDK lease tokens.

## Endpoints

| Method | Path | Auth | Body | Data |
|--------|------|------|------|------|
| `POST` | `/admin/auth/challenge` | None | `WalletAuthChallengeRequest` | `WalletAuthChallengeResponse` |
| `POST` | `/admin/auth/login` | SIWE signature body | `WalletAuthLoginRequest` | `WalletAuthLoginResponse` |
| `GET` | `/admin/auth/status` | Optional bearer | none | `WalletAuthStatusResponse` |
| `POST` | `/admin/auth/logout` | Bearer | none | `{}` |
| `GET` | `/admin/state` | Bearer | none | `AdminStateResponse` |
| `POST` | `/admin/settings` | Bearer | `AdminSettings` | `AdminSettings` |
| `POST` | `/admin/lease-policy` | Bearer | `AdminLeasePolicy` | `{}` |
| `POST` | `/admin/ip-policy` | Bearer | `AdminIPPolicy` | `{}` |

## Auth Payloads

`WalletAuthChallengeRequest`:

| Field | Type | Required |
|-------|------|----------|
| `address` | `string` | yes |

`WalletAuthChallengeResponse`:

| Field | Type |
|-------|------|
| `challenge_id` | `string` |
| `expires_at` | `string` |
| `siwe_message` | `string` |

`WalletAuthLoginRequest`:

| Field | Type | Required |
|-------|------|----------|
| `challenge_id` | `string` | yes |
| `siwe_message` | `string` | yes |
| `siwe_signature` | `string` | yes |

`WalletAuthLoginResponse`:

| Field | Type |
|-------|------|
| `access_token` | `string` |
| `wallet_address` | `string` |

`WalletAuthStatusResponse`:

| Field | Type | Notes |
|-------|------|-------|
| `authenticated` | `boolean` | true only when a valid bearer token was sent |
| `wallet_address` | `string` | omitted when unauthenticated |

## State

`GET /admin/state` returns the full operator view:

| Field | Type |
|-------|------|
| `settings` | `AdminSettings` |
| `leases` | `AdminLease[]` |

`AdminLease` uses the shared `Lease` fields from [API Reference](/api-reference#shared-types)
and adds:

| Field | Type | Notes |
|-------|------|-------|
| `identity_key` | `string` | normalized `name:address` key |
| `address` | `string` | normalized Ethereum address |
| `bps` | `number` | bytes per second limit, `0` means unlimited |
| `client_ip` | `string` | relay-observed client IP |
| `reported_ip` | `string` | client-reported public IP, when present |
| `is_approved` | `boolean` | effective approval result |
| `is_banned` | `boolean` | identity is banned |
| `is_denied` | `boolean` | identity is denied |
| `is_ip_banned` | `boolean` | observed client IP is banned |

## Settings

Settings are written as one object through `POST /admin/settings` and returned
in the same shape:

```json
{
  "approval_mode": "manual",
  "landing_page_enabled": true,
  "udp": {
    "enabled": true,
    "max_leases": 10
  },
  "tcp_port": {
    "enabled": false,
    "max_leases": 0
  }
}
```

`max_leases` must be non-negative. `0` means unlimited.

Supported modes:

| Mode | Behavior |
|------|----------|
| `auto` | active leases can route unless banned or denied |
| `manual` | active leases route only after approval |

## Lease Policy

`POST /admin/lease-policy` accepts a partial policy update for one identity:

| Field | Type | Effect |
|-------|------|--------|
| `identity_key` | `string` | normalized `name:address` key |
| `is_banned` | `boolean` | ban or unban identity registration and renewal |
| `is_approved` | `boolean` | approve or revoke explicit approval |
| `is_denied` | `boolean` | deny or remove denial; `true` also revokes approval |
| `bps` | `number` | set bytes-per-second limit; `0` removes the limit |

Lease policy updates persist to the admin state file and return `{}` on success.

## IP Policy

`POST /admin/ip-policy` accepts:

```json
{ "ip": "203.0.113.10", "is_banned": true }
```

The IP must parse as a valid IPv4 or IPv6 address.
