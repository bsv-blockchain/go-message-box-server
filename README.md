<div align="center">

# 📬&nbsp;&nbsp;go-message-box-server

**Peer-to-peer message storage and delivery for BSV identities, with BRC-31 authentication and BRC-29 payments.**

<br/>

<a href="https://github.com/bsv-blockchain/go-message-box-server/releases"><img src="https://img.shields.io/github/release-pre/bsv-blockchain/go-message-box-server?include_prereleases&style=flat-square&logo=github&color=black" alt="Release"></a>
<a href="https://golang.org/"><img src="https://img.shields.io/github/go-mod/go-version/bsv-blockchain/go-message-box-server?style=flat-square&logo=go&color=00ADD8" alt="Go Version"></a>
<a href="https://github.com/bsv-blockchain/go-message-box-server/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-OpenBSV-blue?style=flat-square" alt="License"></a>

<br/>

<table align="center" border="0">
  <tr>
    <td align="right">
       <code>CI / CD</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/bsv-blockchain/go-message-box-server/actions"><img src="https://img.shields.io/github/actions/workflow/status/bsv-blockchain/go-message-box-server/fortress.yml?branch=main&label=build&logo=github&style=flat-square" alt="Build"></a>
       <a href="https://github.com/bsv-blockchain/go-message-box-server/actions"><img src="https://img.shields.io/github/last-commit/bsv-blockchain/go-message-box-server?style=flat-square&logo=git&logoColor=white&label=last%20update" alt="Last Commit"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Quality</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://codecov.io/gh/bsv-blockchain/go-message-box-server"><img src="https://codecov.io/gh/bsv-blockchain/go-message-box-server/branch/main/graph/badge.svg?style=flat-square" alt="Coverage"></a>
    </td>
  </tr>

  <tr>
    <td align="right">
       <code>Security</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://scorecard.dev/viewer/?uri=github.com/bsv-blockchain/go-message-box-server"><img src="https://api.scorecard.dev/projects/github.com/bsv-blockchain/go-message-box-server/badge?style=flat-square" alt="Scorecard"></a>
       <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/policy-active-success?style=flat-square&logo=security&logoColor=white" alt="Security"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Community</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/bsv-blockchain/go-message-box-server/graphs/contributors"><img src="https://img.shields.io/github/contributors/bsv-blockchain/go-message-box-server?style=flat-square&color=orange" alt="Contributors"></a>
       <a href="https://deepwiki.com/bsv-blockchain/go-message-box-server"><img src="https://deepwiki.com/badge.svg" alt="Ask DeepWiki"></a>
    </td>
  </tr>
</table>

</div>

<br/>
<br/>

<div align="center">

### <code>Project Navigation</code>

</div>

<table align="center">
  <tr>
    <td align="center" width="33%">
       📖&nbsp;<a href="#-overview"><code>Overview</code></a>
    </td>
    <td align="center" width="33%">
       📦&nbsp;<a href="#-installation"><code>Installation</code></a>
    </td>
    <td align="center" width="33%">
       🚀&nbsp;<a href="#-quick-start"><code>Quick&nbsp;Start</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       📚&nbsp;<a href="#-documentation"><code>Documentation</code></a>
    </td>
    <td align="center">
       🏗️&nbsp;<a href="#-architecture"><code>Architecture</code></a>
    </td>
    <td align="center">
       🧪&nbsp;<a href="#-examples--tests"><code>Examples&nbsp;&&nbsp;Tests</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       ⚡&nbsp;<a href="#-benchmarks"><code>Benchmarks</code></a>
    </td>
    <td align="center">
       🛠️&nbsp;<a href="#-code-standards"><code>Code&nbsp;Standards</code></a>
    </td>
    <td align="center">
       🤖&nbsp;<a href="#-ai-usage--assistant-guidelines"><code>AI&nbsp;Usage</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🤝&nbsp;<a href="#-contributing"><code>Contributing</code></a>
    </td>
    <td align="center">
       👥&nbsp;<a href="#-maintainers"><code>Maintainers</code></a>
    </td>
    <td align="center">
       📝&nbsp;<a href="#-license"><code>License</code></a>
    </td>
  </tr>
</table>
<br/>

## 📖 Overview

**go-message-box-server** is a Go reimplementation of the [BSV MessageBox Server](https://github.com/bsv-blockchain/message-box-server). It stores and delivers messages between BSV identity keys: every request is mutually authenticated with BRC-31 (Authrite), delivery can be priced and paid with BRC-29, and the HTTP API is compatible with the TypeScript server and [`@bsv/message-box-client`](https://www.npmjs.com/package/@bsv/message-box-client).

### Core Capabilities
- **Message Delivery**: Send, list (paginated), and acknowledge messages across named message boxes per identity
- **Authentication & Payments**: BRC-31 mutual auth and BRC-29 payments through `go-bsv-middleware`, backed by a `go-wallet-toolbox` wallet
- **Permissions & Fees**: Per-sender or box-wide allow, block, or pay rules, server delivery fees, and delivery price quotes
- **Push Notifications**: Firebase Cloud Messaging (FCM) delivery to the devices registered for each (identity key, token) pair
- **Pluggable Storage**: SQLite, PostgreSQL, or MongoDB behind one `storage.Store` interface, held to a shared conformance suite
- **Paymail Profile Lookup**: An optional `handle@domain` registry that serves self-signed BRC-52 profile certificates

### Key Dependencies

| Node.js Original                  | Go Equivalent                                                                       |
|-----------------------------------|-------------------------------------------------------------------------------------|
| `@bsv/sdk`                        | `github.com/bsv-blockchain/go-sdk`                                                  |
| `@bsv/auth-express-middleware`    | `github.com/bsv-blockchain/go-bsv-middleware` (auth)                                |
| `@bsv/payment-express-middleware` | `github.com/bsv-blockchain/go-bsv-middleware` (payment)                             |
| Express.js                        | `net/http` (Go 1.22+ routing)                                                       |
| Knex + MySQL                      | `pkg/storage` over `database/sql` (SQLite/PostgreSQL) or MongoDB                    |
| —                                 | `github.com/bsv-blockchain/go-wallet-toolbox` (wallet)                              |

### Differences from the Original
1. **Database**: SQLite instead of MySQL by default, with PostgreSQL and MongoDB also supported
2. **WebSockets**: Not yet implemented (the HTTP API is fully compatible)

<br/>

## 📦 Installation

**go-message-box-server** requires a [supported release of Go](https://golang.org/doc/devel/release.html#policy) and a C toolchain, since the SQLite driver uses CGO.

Run it as a server by building from source (see [Quick Start](#-quick-start)) or with Docker. To embed it, add the module to your own:

```shell script
go get -u github.com/bsv-blockchain/go-message-box-server
```

`pkg/config`, `pkg/handlers` and `pkg/storage` are exported so the server can be embedded. An embedder that wants its own persistence implements `storage.Store` and passes it to `handlers.NewServer`; running `storagetest.RunStoreTests` against it is how to know it is correct.

> **Good to know:** Go ignores `replace` directives in dependencies, and `go-wallet-toolbox` needs a few. Copy the `replace` block at the end of this repository's [go.mod](go.mod) into yours. For the same reason, `go install …@latest` cannot build the server; build it from source instead.

<br/>

## 🚀 Quick Start

### Prerequisites

- **Go 1.27+** (see [go.mod](go.mod) for the exact version) with CGO enabled
- A 64-character hex **server private key** for the server's identity
- **Docker** (optional), for PostgreSQL, MongoDB, or a containerized server

### Build from Source

```bash
git clone https://github.com/bsv-blockchain/go-message-box-server.git
cd go-message-box-server
go build -o messagebox-server ./cmd/server
```

### Configure

The server reads its configuration from environment variables; it does not load a `.env` file itself. [.env.example](.env.example) lists them, so copy it, set `SERVER_PRIVATE_KEY` (64-char hex), and export it into your shell:

```bash
cp .env.example .env
set -a; . ./.env; set +a
```

`SERVER_PRIVATE_KEY` is the only required setting. Without the others the server uses SQLite (`messagebox.db`) for messages and a local SQLite wallet. See the [configuration reference](#-documentation) for every variable.

### Run

```bash
./messagebox-server
```

Or try it with a throwaway identity and no `.env` at all:

```bash
SERVER_PRIVATE_KEY=$(openssl rand -hex 32) ./messagebox-server
```

The API listens on port `8080` in development (`3000` when `NODE_ENV` is not `development`), and the Swagger UI is served at `/swagger/index.html`.

### Docker

```bash
docker build -t go-message-box-server .
docker run -p 8080:8080 -e SERVER_PRIVATE_KEY=your-hex-key go-message-box-server
```

Or bring up the server with a database using Docker Compose:

```bash
docker compose up                                           # PostgreSQL
STORAGE_BACKEND=mongo docker compose --profile mongo up      # MongoDB
```

Tagged releases also publish an image to the GitHub Container Registry as `ghcr.io/bsv-blockchain/go-message-box-server`.

<br/>

## 📚 Documentation

- **API Reference**: Browse the endpoints in the Swagger UI at `/swagger/index.html`, or read the [OpenAPI spec](docs/swagger.yaml)
- **Package Docs**: Dive into the godocs at [pkg.go.dev/github.com/bsv-blockchain/go-message-box-server](https://pkg.go.dev/github.com/bsv-blockchain/go-message-box-server)
- **Design Notes**: Read the [paymail profile lookup design spec](docs/specs/2026-09-18-paymail-profile-lookup-design.md)
- **Test Suite**: Review the [conformance suite](pkg/storage/storagetest), the fuzz tests (powered by [`testify`](https://github.com/stretchr/testify)), and the [Jest integration tests](test-client)

### API Endpoints

All endpoints below require BRC-31 authentication via the `go-bsv-middleware` auth middleware. The optional paymail profile lookup adds public, unauthenticated routes alongside them.

| Method | Path                  | Description                                               |
|--------|-----------------------|-----------------------------------------------------------|
| POST   | `/sendMessage`        | Send a message to one or more recipients' message boxes   |
| POST   | `/listMessages`       | List messages from a specific message box                 |
| POST   | `/acknowledgeMessage` | Acknowledge (delete) received messages                    |
| POST   | `/registerDevice`     | Register a device for FCM push notifications              |
| POST   | `/unregisterDevice`   | Remove the caller's registration of an FCM token          |
| GET    | `/devices`            | List registered devices                                   |
| POST   | `/permissions/set`    | Set a message permission (block, allow, or require payment) |
| GET    | `/permissions/get`    | Get the permission for a sender/box combination           |
| GET    | `/permissions/list`   | List all permissions with pagination                      |
| GET    | `/permissions/quote`  | Get a delivery price quote for one or more recipients     |

<br/>

<details>
<summary><strong><code>Configuration Reference</code></strong></summary>
<br/>

| Variable                        | Default                      | Description                                                                                       |
|---------------------------------|------------------------------|---------------------------------------------------------------------------------------------------|
| `SERVER_PRIVATE_KEY`            | (required)                   | Hex-encoded private key for the server identity                                                   |
| `NODE_ENV`                      | `development`                | Environment (`development`, `production`)                                                         |
| `PORT`                          | `8080` (dev) / `3000` (prod) | HTTP listen port                                                                                  |
| `ROUTING_PREFIX`                | ``                           | URL prefix for all authenticated routes                                                           |
| `STORAGE_BACKEND`               | `sql`                        | Storage backend (`sql`, `mongo`)                                                                  |
| `DB_DRIVER`                     | `sqlite3`                    | SQL driver (`sqlite3`, `postgres`)                                                                |
| `DB_SOURCE`                     | `messagebox.db`              | SQL connection string or file path                                                                |
| `MONGO_URI`                     | `mongodb://localhost:27017`  | MongoDB connection URI                                                                            |
| `MONGO_DATABASE`                | `messagebox`                 | MongoDB database name                                                                             |
| `BSV_NETWORK`                   | `mainnet`                    | BSV network (`mainnet`, `testnet`, `ttn`, `tstn`); an unknown value fails startup                  |
| `WALLET_STORAGE_URL`            | unset                        | Remote wallet storage server; unset keeps the wallet in a local SQLite file                       |
| `FIREBASE_PROJECT_ID`           | unset                        | Firebase project for FCM push notifications; unset disables push                                  |
| `FIREBASE_SERVICE_ACCOUNT_JSON` | unset                        | Service account credentials as JSON (takes precedence over the path)                             |
| `FIREBASE_SERVICE_ACCOUNT_PATH` | unset                        | Path to a service account credentials file                                                        |
| `ENABLE_WEBSOCKETS`             | `true`                       | Enable WebSocket support (not yet implemented)                                                    |

`POST /listMessages` pagination reads four more, all optional and matching the TS reference server's "standard" resource profile defaults:

| Variable                  | Default           | Description                                                                                                                                                                                         |
|---------------------------|-------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `LIST_DEFAULT_LIMIT`      | `1000`            | Page size used when a request omits `limit`                                                                                                                                                         |
| `LIST_MAX_LIMIT`          | `1000`            | Largest `limit` a request may ask for; `-1` means no cap                                                                                                                                            |
| `LIST_MAX_OFFSET`         | `100000`          | Largest `offset`/`skip` a request may ask for; `-1` means no cap                                                                                                                                    |
| `LIST_MAX_RESPONSE_BYTES` | `8388608` (8 MiB) | Response byte budget for one page's `messages` array; `-1` means no cap. A single message that alone exceeds the budget fails the request with `413 ERR_MESSAGE_RESPONSE_TOO_LARGE` rather than being silently dropped |

`LIST_DEFAULT_LIMIT` must not exceed `LIST_MAX_LIMIT` (when the latter is capped); the process refuses to start otherwise.

The optional paymail profile lookup reads its own settings, listed under **Paymail Profile Lookup** below.

</details>

<details>
<summary><strong><code>Storage Backends</code></strong></summary>
<br/>

Persistence sits behind the `storage.Store` interface in `pkg/storage`. Pick a backend with `STORAGE_BACKEND`:

| Backend          | Value   | Configured by                                    |
|------------------|---------|--------------------------------------------------|
| SQLite (default) | `sql`   | `DB_DRIVER=sqlite3`, `DB_SOURCE=messagebox.db`   |
| PostgreSQL       | `sql`   | `DB_DRIVER=postgres`, `DB_SOURCE=postgres://...` |
| MongoDB          | `mongo` | `MONGO_URI`, `MONGO_DATABASE`                    |

The schema is created on startup and the data it holds is the same either way:

- **message boxes**: Named message boxes per identity key
- **messages**: Stored messages with sender, recipient, body
- **message permissions**: Per-sender or box-wide fee/block settings
- **server fees**: Server-level delivery fees per box type
- **device registrations**: FCM tokens for push notifications, one per (identity key, token) pair

The SQL backend keeps the original relational shape, including the `messageBox` table and its integer foreign key; MongoDB stores the box name on the message instead. Neither detail is visible through the interface or the HTTP API.

`storage.DeviceStore` gained `UnregisterDevice(ctx, identityKey, fcmToken)`, so a `storage.Store` implemented outside this repository has to add it; the conformance suite checks it.

`storage.MessagePager` is an optional extension a backend may implement to answer `POST /listMessages` pagination directly instead of the handler paging in memory over `ListMessages`'s full result. It is not part of `storage.Store` (adding a required method there would break any external `Store` implementation plugged in against this package), so the handler type-asserts for it and falls back to correct, if less efficient, in-memory pagination when a backend does not implement it. Both bundled backends (SQLite/PostgreSQL and MongoDB) implement it.

</details>

<details>
<summary><strong><code>Devices & Push Notifications</code></strong></summary>
<br/>

A device registration belongs to the authenticated identity **and** the token together. A wallet with several profiles runs one FCM token for all of them and registers it once per profile, so one token can be registered by several identities at once, each pushed to separately. Registering the same pair again is idempotent and returns the same `deviceId`. `POST /unregisterDevice` with `{"fcmToken": "..."}` deletes only the caller's registration of a token and succeeds when there is none. FCM reporting a token dead deactivates it for every identity that registered it.

The push `data` carries `messageId`, `originator`, `recipient` (the identity key the message was sent to) and `messageBox`, on both Android and APNs. Because a token can serve several identities, a client uses `recipient` to tell which one a push is for. The notification title and body are unchanged.

**Upgrading** an existing database is automatic and keeps every registration and its `deviceId`. SQLite rebuilds `device_registrations` in one transaction; PostgreSQL replaces the `UNIQUE(fcm_token)` constraint with a unique index on `(identity_key, fcm_token)`; MongoDB backfills an `fcmToken` field from each legacy document's `_id` and builds a unique index on `(identityKey, fcmToken)`.

Replace replicas rather than running old and new side by side. Replicas still on the previous release cannot register devices on PostgreSQL (their `ON CONFLICT (fcm_token)` has no key left) until they are replaced. On MongoDB a document an old replica writes in that window is picked up by the next boot's backfill; until then it is the same registration twice, and this release lists and unregisters the pair once. A registration made through this release is keyed by an ObjectID rather than by the token, which an old replica cannot read as a token: it fails to list that identity's devices, so it sends that identity no push and answers its `GET /devices` with an error until it is replaced, but it never deactivates or changes the registration, and nothing needs re-registering afterwards. Rolling back to the previous release has the same effect for every identity that registered since the upgrade: it can neither list nor push to them, and nothing is deactivated or lost, so rolling forward restores delivery.

</details>

<details>
<summary><strong><code>Paymail Profile Lookup</code></strong></summary>
<br/>

An optional handle registry that maps `handle@domain` to an identity key and serves a self-signed BRC-52 profile certificate for it, discovered the paymail way (`_bsvalias._tcp` SRV record plus `/.well-known/bsvalias`). The server is a registry and a cache only: it cannot forge a profile, and clients must verify every certificate they receive; see [the design spec](docs/specs/2026-09-18-paymail-profile-lookup-design.md).

The feature is off until `PAYMAIL_DOMAIN` is set, and it **requires `STORAGE_BACKEND=mongo`**: with `PAYMAIL_DOMAIN` set on any other backend the server refuses to start rather than come up without the registry. Production points `MONGO_URI` at a MongoDB Atlas `mongodb+srv://…` URI; local development uses MongoDB Community:

```bash
docker compose --profile mongo up -d mongo
```

#### Configuration

| Env                    | Meaning                                                                                                                                                                                                                                                                           | Default                          |
|------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------|
| `PAYMAIL_DOMAIN`       | Domain served, e.g. `example.com`. Unset → feature off, no routes mounted                                                                                                                                                                                                          | unset                            |
| `PAYMAIL_HOST`         | Public base URL used in capability templates, e.g. `https://mb.example.com`                                                                                                                                                                                                       | required when domain set         |
| `HANDLE_COOLDOWN_DAYS` | Days before a released handle may be claimed by a different key                                                                                                                                                                                                                   | `30`                             |
| `ADMIN_IDENTITY_KEYS`  | Comma list of compressed pubkey hex allowed to call admin routes                                                                                                                                                                                                                  | empty (admin routes reject all)  |
| `LOOKUP_RATE_PER_MIN`  | Per-IP cap, shared across all five public routes; exactly `0` **disables** the limiter (it does not block traffic). Any other value that is not a non-negative integer, a negative one included, falls back to the default                                                         | `60`                             |
| `TRUST_PROXY`          | `true` → client IP taken from `X-Forwarded-For` instead of the connection (set when behind a load balancer)                                                                                                                                                                       | `false`                          |
| `TRUSTED_PROXY_HOPS`   | How many proxies of your own sit in front of this process; the client's address is counted that many entries from the right of `X-Forwarded-For`. Only read when `TRUST_PROXY=true`; values below `1` are read as `1`                                                              | `1`                              |
| `CLIENT_IP_HEADER`     | Name of a single-valued header a proxy sets to the real client address, e.g. `CF-Connecting-IP` behind Cloudflare. Must be a syntactically valid HTTP header field name or the process refuses to start. **Wins over `TRUST_PROXY`/`TRUSTED_PROXY_HOPS`** whenever the header is present exactly once and its value parses as an IP; otherwise falls back to the `TRUST_PROXY` logic above. See the caveat below | unset |

The rate limiter is in-memory and therefore per-replica: three replicas behind one load balancer allow three times `LOOKUP_RATE_PER_MIN` between them. Behind a load balancer every request also arrives from the balancer's own address, so without `TRUST_PROXY=true` the entire fleet shares a single bucket.

Set `TRUSTED_PROXY_HOPS` to the number of proxies you run in front of the server: one for a single load balancer, two for a CDN in front of it. Proxies *append* to `X-Forwarded-For` rather than rewriting it (AWS ALB, Google Cloud Load Balancing, Cloudflare and nginx's `$proxy_add_x_forwarded_for` all do), so the leftmost entry is whatever the client chose to send and only the entry your own nearest proxies appended means anything. Counting from the right is what stops a client picking a fresh bucket for every request, or spending another client's. A header with fewer entries than there are hops falls back to the connection address, which over-counts rather than letting traffic past, so a hop count that is too high is safe, and one that is too low is not.

`TRUST_PROXY` assumes the proxy in front of the process *appends* to `X-Forwarded-For`. Some proxies don't: Traefik without `forwardedHeaders.trustedIPs` configured *replaces* the header with the address it saw the connection from, so counting hops from the right just recovers the proxy's own address no matter how it's set. If that proxy (or something upstream of it, e.g. Cloudflare) instead forwards a dedicated single-value header untouched, point `CLIENT_IP_HEADER` at it: `CF-Connecting-IP` for Cloudflare. When both are set, `CLIENT_IP_HEADER` wins whenever it is present exactly once and parses as an IP; `TRUST_PROXY` is only consulted as the fallback.

> **Security caveat:** `CLIENT_IP_HEADER` is only trustworthy when the origin is reachable *exclusively* through the proxy that sets it. Anything that can reach the process directly can set the header itself, and because every distinct value is its own bucket, a caller sending a fresh value on each request gets a fresh bucket each time: it is not rate limited **at all**, and it adds an entry per request to the limiter's in-memory map until the minute rolls. That is *weaker* than leaving the variable unset, where the same traffic keys on one connection address and is capped at `LOOKUP_RATE_PER_MIN`. So set it only once the origin accepts nothing but the proxy, such as Cloudflare's published IP ranges, or a tunnel with no other route in. A common way for this not to hold: an ingress controller whose load balancer is internet-facing *and* fronts a Cloudflare tunnel, so the same route answers requests that never touched Cloudflare. Close that first, for instance with an ingress-level source allow-list that admits only the tunnel's addresses on this host. The header bounds fairness between clients that really do arrive through the proxy; it is **not** an authentication signal and must never be used as one.

#### Routes

| Method | Path                             | Auth                            | Purpose                                                    |
|--------|----------------------------------|---------------------------------|------------------------------------------------------------|
| GET    | `/.well-known/bsvalias`          | none                            | Capability discovery document                              |
| PUT    | `/api/handle`                    | the certificate itself          | Register, update or release a handle                       |
| GET    | `/api/handle/{query}`            | none                            | Search, ranked, max 10 (capability `0ace65da5987`)         |
| GET    | `/api/handle/available/{handle}` | none                            | Advisory availability check                                |
| GET    | `/api/identityKey/{pubkey}`      | none                            | Reverse lookup (capability `43dcf83ddc5f`)                 |
| POST   | `/admin/handle/release`          | BRC-104 + `ADMIN_IDENTITY_KEYS` | Operator release after lost keys                           |

The public routes carry no session auth by design: a `PUT` is authorized by the certificate in its body, a statement the identity key signed naming the handle. They sit outside the auth and payment middleware and are mounted at fixed paths; `ROUTING_PREFIX` applies to the admin route only.

#### DNS

```
_bsvalias._tcp.example.com. 3600 IN SRV 10 10 443 mb.example.com.
```

The zone must be DNSSEC-signed: clients reject an unsigned SRV answer, because an attacker who can forge it chooses which server answers for the domain. `PAYMAIL_HOST` must be the HTTPS origin the SRV target serves (`https://mb.example.com` above): it is what the capability document hands clients as the base of every lookup URL, so a client that took the trouble to reach a signed SRV target may refuse a plain-http URL. The server logs a warning at startup when `PAYMAIL_HOST` is neither an `https://` origin nor loopback. Without an SRV record clients fall back to `https://example.com:443/.well-known/bsvalias`.

#### Client Verification

Normative for anyone implementing a resolver:

1. DNSSEC + SRV `_bsvalias._tcp.<domain>` → host; GET well-known; find capability.
2. For every certificate received: check `type`, `subject == certifier`, signature, `fields.paymail` ends with `@<queried domain>`, no `released`.
3. Search results are suggestions. Show full `fields.paymail`; user selects. Never auto-select `result[0]`. If the user entered a complete `handle@domain`, require `fields.paymail` to equal it exactly.
4. Reverse lookup: additionally require `subject ==` queried key; optionally forward-resolve `fields.paymail` and compare.

</details>

<details>
<summary><strong><code>Wallet</code></strong></summary>
<br/>

The server's wallet is built with `go-wallet-toolbox`. It provides:

- BRC-31 authentication via `go-bsv-middleware`
- BRC-29 payment processing
- Identity key derivation from `SERVER_PRIVATE_KEY`
- Automatic storage migration on startup

Wallet storage is a local SQLite file by default, or a remote storage server when `WALLET_STORAGE_URL` is set. The network is configurable via `BSV_NETWORK` (`mainnet`, `testnet`, `ttn`, `tstn`).

</details>

<details>
<summary><strong><code>Development Build Commands</code></strong></summary>
<br/>

Get the [MAGE-X](https://github.com/mrz1836/mage-x) build tool for development:
```shell script
go install github.com/mrz1836/mage-x/cmd/magex@latest
```

View all build commands

```bash script
magex help
```

</details>

<details>
<summary><strong>Repository Features</strong></summary>
<br/>

This repository includes 25+ built-in features covering CI/CD, security, code quality, developer experience, and community tooling.

**[View the full Repository Features list →](.github/docs/repository-features.md)**

</details>

<details>
<summary><strong><code>Releases & Docker Images</code></strong></summary>
<br/>

This project uses [goreleaser](https://github.com/goreleaser/goreleaser) for streamlined releases to GitHub. To get started, install it via:

```bash
brew install goreleaser
```

The release process is defined in the [.goreleaser.yml](.goreleaser.yml) configuration file.

Then create and push a new Git tag using:

```bash
magex version:bump push=true bump=patch branch=main
```

Pushing a `v*` tag also builds the [Dockerfile](Dockerfile) and publishes the image to `ghcr.io/bsv-blockchain/go-message-box-server`, tagged with the full version, the major.minor version, and the commit SHA ([workflow](.github/workflows/docker-publish.yml)).

</details>

<details>
<summary><strong><code>Pre-commit Hooks</code></strong></summary>
<br/>

Set up the Go-Pre-commit System to run the same formatting, linting, and tests defined in [AGENTS.md](.github/AGENTS.md) before every commit:

```bash
go install github.com/mrz1836/go-pre-commit/cmd/go-pre-commit@latest
go-pre-commit install
```

The system is configured via modular environment files in [`.github/env/`](.github/env/README.md) and provides 17x faster execution than traditional Python-based pre-commit hooks. See the [complete documentation](http://github.com/mrz1836/go-pre-commit) for details.

</details>

<details>
<summary><strong>GitHub Workflows</strong></summary>
<br/>

All workflows are driven by modular configuration in [`.github/env/`](.github/env/README.md); no YAML editing required.

**[View all workflows and the control center →](.github/docs/workflows.md)**

</details>

<details>
<summary><strong><code>Updating Dependencies</code></strong></summary>
<br/>

To update all dependencies (Go modules, linters, and related tools), run:

```bash
magex deps:update
```

This command ensures all dependencies are brought up to date in a single step, including Go modules and any tools managed by [MAGE-X](https://github.com/mrz1836/mage-x). It is the recommended way to keep your development environment and CI in sync with the latest versions.

</details>

<br/>

## 🏗️ Architecture

```
cmd/server/         - Entry point, storage backend selection, middleware wiring, CORS
pkg/
  config/           - Environment variable loading
  handlers/         - HTTP route handlers and fee/notification policy
  handles/          - Handle validation, look-alike skeletons, BRFC ids
  profilecert/      - Public profile certificate parsing and verification
  storage/          - Backend-neutral persistence interface and domain types
    sqlstore/       - SQLite and PostgreSQL implementation
    mongostore/     - MongoDB implementation, plus the handle registry
    storagetest/    - Conformance suite every implementation must pass
internal/
  firebase/         - FCM push notification delivery
  logger/           - Toggleable structured logger
docs/               - Generated OpenAPI (Swagger) spec and design notes
test-client/        - Jest integration tests (TypeScript)
```

A request passes through CORS, then either one of the public routes (the Swagger UI, or the paymail lookup behind its rate limiter) or the BRC-31 auth and BRC-29 payment middleware in front of the API handlers.

<br/>

## 🧪 Examples & Tests

All unit and fuzz tests run via [GitHub Actions](https://github.com/bsv-blockchain/go-message-box-server/actions) and use [Go version 1.27.x](https://go.dev/doc/go1.27). View the [configuration file](.github/workflows/fortress.yml).

Run all tests (fast):

```bash script
magex test
```

Run all tests with race detector (slower):
```bash script
magex test:race
```

Run the fuzz tests, which cover handle folding and validation, profile certificate parsing, rate-limiter client IP derivation, pagination bounds, and SQL placeholder rewriting:
```bash script
magex test:fuzz
```

<details>
<summary><strong><code>Storage Backend Tests</code></strong></summary>
<br/>

`pkg/storage/storagetest` holds the conformance suite. SQLite runs it on every `go test ./...`; PostgreSQL and MongoDB run it when pointed at a database:

```bash
POSTGRES_TEST_DSN='postgres://user:pass@localhost:5432/messagebox_test?sslmode=disable' \
  go test ./pkg/storage/sqlstore/
MONGO_TEST_URI='mongodb://localhost:27017' go test ./pkg/storage/mongostore/
```

Both drop their tables/collections first, so point them at a throwaway database. In CI, the [MongoDB Store](.github/workflows/mongodb-store.yml) workflow runs the MongoDB suite against a live service container.

</details>

<details>
<summary><strong><code>Jest Integration Tests</code></strong></summary>
<br/>

The `test-client/` directory contains 30 integration tests against a running server: `messagebox.test.ts` drives the message lifecycle through `@bsv/message-box-client`, and `devices-permissions.test.ts` drives the device, permission and quote endpoints over `AuthFetch` directly (the client does not cover those routes). Both use an `@bsv/sdk` `ProtoWallet` for BRC-31 auth.

Point them at a server on any backend. The responses are byte-identical, which is a property the conformance suite enforces rather than a hope: timestamps are written UTC so a non-UTC host cannot skew them, and the permission list is ordered bytewise so PostgreSQL's libc collation cannot reorder a page.

```bash
# Terminal 1: Start the server
SERVER_PRIVATE_KEY=$(openssl rand -hex 32) go run ./cmd/server

# Terminal 2: Run tests
cd test-client
npm ci
npx jest --verbose
```

Set `MESSAGEBOX_HOST` to test a server somewhere other than `http://localhost:8080`.

**Tests cover:**
- Send message (plaintext, JSON body, send-to-self)
- List messages (populated box, empty box)
- Acknowledge messages (valid, already-acknowledged, nonexistent)
- Register and list devices (upsert on re-registration, platform validation)
- Two identities registering one token, and unregistering (idempotent, scoped to the caller)
- Set, get and list permissions (box-wide vs sender-specific, order, paging, filtering)
- Delivery quotes
- Input validation (empty recipient, empty body)
- Multiple messages in the same box

All tests use real BRC-31 AuthFetch authentication against the running server.

</details>

<br/>

## ⚡ Benchmarks

Run the Go benchmarks:

```bash script
magex bench
```

> **Note:** The project has no benchmark suite yet. The current focus is correctness across storage backends and wire compatibility with the TypeScript server; benchmarks for the message and storage paths are planned.

<br/>

## 🛠️ Code Standards
Read more about this Go project's [code standards](.github/CODE_STANDARDS.md).

<br/>

## 🤖 AI Usage & Assistant Guidelines
Read the [AI Usage & Assistant Guidelines](.github/tech-conventions/ai-compliance.md) for details on how AI is used in this project and how to interact with AI assistants.

<br/>

## 👥 Maintainers
| [<img src="https://github.com/sirdeggen.png" height="50" alt="Deggen" />](https://github.com/sirdeggen) | [<img src="https://github.com/icellan.png" height="50" alt="Siggi" />](https://github.com/icellan) | [<img src="https://github.com/dzolt.png" height="50" alt="Damian" />](https://github.com/dzolt) | [<img src="https://github.com/mrz1836.png" height="50" width="50" alt="MrZ" />](https://github.com/mrz1836) |
|:-------------------------------------------------------------------------------------------------------:|:--------------------------------------------------------------------------------------------------:|:-----------------------------------------------------------------------------------------------:|:-----------------------------------------------------------------------------------------------------------:|
|                                 [Deggen](https://github.com/sirdeggen)                                  |                                [Siggi](https://github.com/icellan)                                 |                               [Damian](https://github.com/dzolt)                                |                                      [MrZ](https://github.com/mrz1836)                                      |

<br/>

## 🤝 Contributing
View the [contributing guidelines](.github/CONTRIBUTING.md) and please follow the [code of conduct](.github/CODE_OF_CONDUCT.md).

### How can I help?
All kinds of contributions are welcome :raised_hands:!
The most basic way to show your support is to star :star2: the project, or to raise issues :speech_balloon:.

[![Stars](https://img.shields.io/github/stars/bsv-blockchain/go-message-box-server?label=Please%20like%20us&style=social&v=1)](https://github.com/bsv-blockchain/go-message-box-server/stargazers)

<br/>

## 📝 License

[![License](https://img.shields.io/badge/license-OpenBSV-blue?style=flat&logo=springsecurity&logoColor=white)](LICENSE)
