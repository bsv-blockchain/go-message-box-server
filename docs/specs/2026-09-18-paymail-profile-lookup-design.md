# Paymail Profile Lookup — Design

Date: 2026-09-18
Status: approved design, pending implementation plan

## Goal

Resolve a human handle (`<handle>@<domain>`) to an identity key plus signed
public profile, and the reverse (identity key → profile), hosted by
go-message-box-server.

Kept from Paymail: DNSSEC, `_bsvalias._tcp` SRV domain → host resolution,
`/.well-known/bsvalias` capability discovery, `handle@domain.tld` format.

New: two capabilities whose response documents are self-signed BRC-52
`Certificate` objects (as serialized by `@bsv/sdk` / go-sdk
`auth/certificates`). The server is a registry and cache; **clients must verify
every certificate themselves**.

## Capabilities (BRFC)

BRFC id = first 12 hex chars of byte-reversed double-SHA256 of
`title + author + version` (go-paymail `BRFCSpec.Generate`).

| Title | Author | Version | ID |
|---|---|---|---|
| `public profile lookup` | `Deggen` | `v1.0.0` | `0ace65da5987` |
| `public profile reverse lookup` | `Deggen` | `v1.0.0` | `43dcf83ddc5f` |

IDs are hardcoded constants; a unit test re-derives them. No go-paymail
dependency in the server (its server package is gin-only; the well-known JSON is
trivial).

```json
{"bsvalias":"1.0","capabilities":{
  "0ace65da5987":"https://<host>/api/handle/{query}",
  "43dcf83ddc5f":"https://<host>/api/identityKey/{pubkey}"
}}
```

## Configuration

| Env | Meaning | Default |
|---|---|---|
| `PAYMAIL_DOMAIN` | Domain served, e.g. `example.com`. Unset → feature off, no routes mounted | unset |
| `PAYMAIL_HOST` | Public base URL used in capability templates, e.g. `https://mb.example.com` | required when domain set |
| `HANDLE_COOLDOWN_DAYS` | Days before a released handle may be claimed by a different key | `30` |
| `ADMIN_IDENTITY_KEYS` | Comma list of compressed pubkey hex allowed to call admin routes | empty (admin routes reject all) |
| `LOOKUP_RATE_PER_MIN` | Per-IP request cap on public routes | `60` |

## Profile certificate

Standard BRC-52 certificate JSON:
`{type, serialNumber, subject, certifier, revocationOutpoint, fields, signature}`.

Rules (server enforces on write; clients re-check all but DB state):

1. `type` = base64 of SHA-256 of the UTF-8 string `public profile lookup` (constant).
2. `subject == certifier` (self-signed). This key **is** the identity key.
3. Signature valid per go-sdk `Certificate.Verify()` (anyone counterparty).
4. Fields are **plaintext** strings (no BRC-52 field encryption, no keyring).
   - Required `paymail`: exact `handle@PAYMAIL_DOMAIN`, lowercase.
   - Required `issuedAt`: RFC 3339 UTC timestamp.
   - Optional `released`: `"true"` marks a tombstone (see Release).
   - Any other fields free-form (displayName, avatar, bio, …).
   - Limits: ≤ 32 fields, each value ≤ 1 KB, whole body ≤ 16 KB.
5. `revocationOutpoint` = zero outpoint
   (`0000…0000.0`). No on-chain revocation; replace or tombstone instead.
6. `serialNumber`: random 32 bytes base64; must differ from the stored one on update.

## Handle rules (`pkg/handles`, pure functions)

- `Validate(handle)`: `^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$` (3–32 chars), not in
  reserved list (`admin`, `administrator`, `root`, `support`, `help`,
  `postmaster`, `abuse`, `security`, `info`, `available`, `api`, `bsvalias`,
  `system`, `official`, `noreply`).
- `Skeleton(handle)`: lowercase → remove `.`, `_`, `-` → fold multi-char
  `rn→m`, `vv→w` → fold single-char `0→o`, `1→l`, `i→l`, `5→s`, `2→z`, `8→b` →
  collapse runs of the same character.
- Reserved words are also compared by skeleton.
- Two handles with equal skeletons are "too similar"; only the first may
  register.

## Storage

One table, added to both sqlite and postgres migration lists in `pkg/db/db.go`;
queries in `pkg/db/queries.go` following existing `*DB` method style.

```
handles(
  handle          TEXT PRIMARY KEY,
  skeleton        TEXT NOT NULL UNIQUE,
  identityKey     TEXT UNIQUE,          -- NULL when released
  lastIdentityKey TEXT NOT NULL,
  certificate     TEXT,                 -- JSON, NULL when released
  issuedAt        TIMESTAMP NOT NULL,   -- highest accepted, survives release
  createdAt, updatedAt TIMESTAMP NOT NULL,
  releasedAt      TIMESTAMP,            -- NULL while active
  cooldownUntil   TIMESTAMP,            -- NULL = no cooldown
  releasedBy      TEXT                  -- 'owner' or admin identity key
)
```

Rows are never deleted (keeps `issuedAt` replay guard and skeleton reservation
during cooldown).

First-come-first-served is enforced by the UNIQUE constraints, never by
check-then-insert. Constraint violations map to 409 codes. Reclaiming a
released row is one conditional `UPDATE … WHERE identityKey IS NULL AND
(lastIdentityKey = ? OR cooldownUntil IS NULL OR cooldownUntil < ?)`; zero rows
affected → 409.

## HTTP API

All public routes mount on `rootMux`, outside the auth/payment wrap, behind the
existing CORS handler plus a per-IP rate limiter and a 16 KB body cap.

### `GET /.well-known/bsvalias`
Capabilities document above.

### `PUT /api/handle` — register / update / release (cert-as-auth)
Body: the certificate JSON. No session auth: the certificate is the
authorisation (signed statement by the identity key naming the handle).
Signature and structural checks run before any DB access.

Outcomes:
- No row for handle → insert (register). `201`.
- Active row, same `subject`, `issuedAt` strictly greater, new `serialNumber`
  → replace cert (update). `200`.
- Same as update with `fields.released == "true"` → tombstone: `identityKey`
  and `certificate` set NULL, `releasedAt = now`,
  `cooldownUntil = now + HANDLE_COOLDOWN_DAYS`, `releasedBy = 'owner'`. `200`.
- Released row → reclaim per conditional UPDATE above; `issuedAt` must still be
  strictly greater than the row's stored value. `201`.
- A key that wants a different handle must tombstone its current one first
  (`ERR_KEY_HAS_HANDLE` otherwise).
- Byte-identical resubmission of the stored cert → `200` no-op.

Errors (`writeError` convention): `400 ERR_INVALID_CERTIFICATE`,
`400 ERR_INVALID_HANDLE`, `400 ERR_WRONG_DOMAIN`, `409 ERR_HANDLE_TAKEN`,
`409 ERR_HANDLE_TOO_SIMILAR`, `409 ERR_HANDLE_RESERVED`,
`409 ERR_KEY_HAS_HANDLE`, `409 ERR_HANDLE_COOLDOWN`, `409 ERR_STALE_CERTIFICATE`
(issuedAt not greater), `413`, `429`.

Replay analysis: current cert replay = no-op; older cert = stale; after release
= stale (issuedAt survives); other domain = wrong domain; third party submitting
a victim's fresh cert registers exactly what the victim signed.

### `GET /api/handle/{query}` — search (capability `0ace65da5987`)
`{query}` is whatever the user has typed: `deg`, `deggen`, `deggen@example.com`.
Lowercased; `@domain` suffix stripped (different domain → `[]`). Min length 2.

Response: `200`, JSON array of certificates (possibly empty), max 10, ranked:
1. exact handle
2. handle prefix (`LIKE 'q%'`)
3. skeleton prefix (`Skeleton(q)` vs skeleton column)
4. handle substring (only when `len(q) ≥ 3`)

Handle-only matching; `displayName` is not searched (non-unique → impersonation
vector). No extensions (pg_trgm etc.) so sqlite and postgres behave the same.

### `GET /api/identityKey/{pubkey}` — reverse lookup (capability `43dcf83ddc5f`)
`{pubkey}` must match `^0[23][0-9a-f]{64}$`. Response: the single certificate,
or `404 ERR_HANDLE_NOT_FOUND`. Per-host only; no cross-domain index.

### `GET /api/handle/available/{handle}`
`200 {"available":bool,"reason":"taken|too_similar|reserved|invalid|cooldown"}`.
Advisory only; `PUT` is the source of truth.

### `POST {prefix}/admin/handle/release` — operator release (BRC-104)
On the existing authed mux. Caller identity (`getIdentityKey`) must be in
`ADMIN_IDENTITY_KEYS`, else `403 ERR_NOT_ADMIN`. Body:
`{"handle":"…","skipCooldown":true}`. For lost-key recovery after out-of-band
KYC. Sets row released with `releasedBy = <admin key>`; `skipCooldown` (default
true) leaves `cooldownUntil` NULL so the user's new key can claim immediately.
Every call logged with admin key, handle, previous identity key.

## Client verification (normative for BRFC docs)

1. DNSSEC + SRV `_bsvalias._tcp.<domain>` → host; GET well-known; find capability.
2. For every certificate received: check `type`, `subject == certifier`,
   signature, `fields.paymail` ends with `@<queried domain>`, no `released`.
3. Search results are suggestions. Show full `fields.paymail`; user selects.
   Never auto-select `result[0]`. If the user entered a complete
   `handle@domain`, require `fields.paymail` to equal it exactly.
4. Reverse lookup: additionally require `subject ==` queried key; optionally
   forward-resolve `fields.paymail` and compare.

Residual trust in the domain operator: which key owns a handle (inherent to
Paymail, and explicit via admin release), and freshness (may serve an older
cert). Operator cannot forge profile fields or rebind a cert to another handle.

## Code layout

- `pkg/handles/` — `Validate`, `Skeleton`, reserved list, BRFC constants + derivation test.
- `pkg/profilecert/` — parse + rule checks 1–6 over go-sdk `Certificate`.
- `pkg/db` — migration + `RegisterHandle`, `UpdateHandleCert`, `ReleaseHandle`,
  `ReclaimHandle`, `SearchHandles`, `GetCertByIdentityKey`, `HandleAvailability`.
- `pkg/handlers/lookup.go` — public handlers; `admin_handle.go` — admin route.
- `pkg/handlers/ratelimit.go` — per-IP token bucket (in-memory).
- `cmd/server/main.go` — mount when `PAYMAIL_DOMAIN` set.
- `README.md` — DNS setup (SRV record, DNSSEC), env vars. Swagger annotations on handlers.

## Testing (stdlib `testing`, sqlite in-memory)

- Table tests: `Validate`, `Skeleton` (each fold, separators, repeats, reserved-by-skeleton).
- BRFC id derivation matches constants.
- `profilecert`: bad sig, subject≠certifier, wrong type, wrong domain, missing
  fields, oversize, non-zero revocation outpoint.
- Handler tests with real go-sdk-signed certs: register, update, stale issuedAt,
  same serial, tombstone, reclaim by same key inside cooldown, reclaim by other
  key blocked then allowed, second handle for same key, similar handle,
  identical resubmit, admin release (non-admin 403, skipCooldown both ways).
- Concurrency: N goroutines register same skeleton → exactly one 201.
- Search ranking order, min length, domain strip, limit 10.
- Well-known document shape; feature off when `PAYMAIL_DOMAIN` unset.

## Out of scope

Client resolver libraries (Go/TS), BRC documents for the two capabilities,
displayName search, edit-distance fuzzy matching, multi-domain hosting, paid
registration, cross-domain reverse lookup.
