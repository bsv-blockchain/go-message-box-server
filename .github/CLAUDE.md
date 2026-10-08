# CLAUDE.md

## 📦 Project Summary

**go-message-box-server** is a Go reimplementation of the [BSV MessageBox Server](https://github.com/bsv-blockchain/message-box-server). It provides peer-to-peer message storage and delivery between BSV identity keys, authenticated with BRC-31 (Authrite) and monetized with BRC-29 payments, and is wire-compatible with the TypeScript reference server and `@bsv/message-box-client`.

### Core Capabilities
- **Message Delivery**: Send, list (paginated), and acknowledge messages across named per-identity message boxes
- **Authentication & Payments**: BRC-31 mutual auth and BRC-29 payments via `go-bsv-middleware`, backed by a `go-wallet-toolbox` wallet
- **Permissions & Fees**: Per-sender or box-wide allow/block/pay rules, server delivery fees, and delivery price quotes
- **Push Notifications**: FCM device registration per (identity key, token) pair with Android and APNs delivery
- **Pluggable Storage**: `storage.Store` interface with SQLite, PostgreSQL, and MongoDB backends, verified by a shared conformance suite (`pkg/storage/storagetest`)
- **Paymail Profile Lookup**: Optional `handle@domain` registry serving self-signed BRC-52 profile certificates (MongoDB only)

### Target Use Cases
Wallet-to-wallet messaging, payment-gated inboxes, and push-notified delivery for BSV applications; embeddable through the exported `pkg/config`, `pkg/handlers`, and `pkg/storage` packages.

## 🤖 Welcome, Claude

This repository uses **`AGENTS.md`** as the single source of truth for:

* Coding conventions (naming, formatting, commenting, testing)
* Contribution workflows (branch prefixes, commit message style, PR templates)
* Release, CI, and dependency‑management policies
* Security reporting and governance links

> **TL;DR:** **Read `AGENTS.md` first.**
> All technical or procedural questions are answered there.

### Quick Checklist for Claude

1. **Study `AGENTS.md`**
   Make sure every automated change or suggestion respects those rules.
2. **Follow branch‑prefix and commit‑message standards**
   They drive Mergify auto‑labeling and CI gates.
3. **Never tag releases**
4. **Pass CI**
   Run `go fmt`, `goimports`, `go vet`, `staticcheck`, and `golangci‑lint` locally before opening a PR.

If you encounter conflicting guidance elsewhere, `AGENTS.md` wins.
Questions or ambiguities? Open a discussion or ping a maintainer instead of guessing.

Happy hacking!
