# YettiQanot (Taxi MVP)

Telegram-first taxi aggregator: **rider bot**, **driver bot**, optional **admin bot**, **Mini App** map, and a single **Go** HTTP + WebSocket service. Production database is **Turso (libSQL / SQLite-compatible)**, not PostgreSQL.

---

## Contents

1. [Overview](#overview)
2. [Prerequisites](#prerequisites)
3. [Configuration](#configuration)
4. [Run locally](#run-locally)
5. [Database and migrations](#database-and-migrations)
6. [HTTP API (reference)](#http-api-reference)
7. [Deployment](#deployment)
8. [Architecture](#architecture)
9. [Fare and commission](#fare-and-commission)
10. [Driver program: promo and referral](#driver-program-promo-and-referral-yettiqanot)
11. [Manual test checklist](#manual-test-checklist)
12. [Developer notes](#developer-notes)

---

## Prerequisites

- **Go:** **1.21+** (see **`go.mod`**; toolchain may pin a newer patch release).
- **Database:** Turso / libSQL URL + auth token (or compatible SQLite), **not** the default in **`Makefile`** (see [Database and migrations](#database-and-migrations)).
- **Docker:** optional, for `docker compose` (Docker Compose V2).

---

## Overview

| Piece | Role |
|--------|------|
| **`cmd/app`** | HTTP API (Gin), Telegram bots (long polling), WebSocket hub, background workers |
| **`internal/services`** | Dispatch, assignment, trip lifecycle, fare, admin, approval notifier |
| **`internal/accounting`** | Promo/cash wallets, **`driver_ledger`** (append-only), commission offsets |
| **`internal/bot/`** | Rider, driver, and optional admin Telegram handlers |
| **`internal/legal`** | Active documents, acceptances, driver/rider compliance checks |
| **`internal/driverloc`** | Shared driver-bot strings and reply-keyboard helpers (e.g. live-location button) |
| **`webapp/`** | Static Mini App (driver map, rider map); can be hosted on Vercel |
| **`db/migrations/`** | Goose migrations (SQLite dialect) |

**Dispatch:** closest-driver priority, timed batches; **`ride_requests`** tracks notify state. **Drivers** are considered for orders when **Telegram live location** is fresh, **balance** (promo + cash) is sufficient, **legal** is OK, and verification is **approved** where required.

---

## Configuration

Create a **`.env`** in the project root (optional: use [godotenv](https://github.com/joho/godotenv) — `internal/config` loads it automatically).

### Required

| Variable | Description |
|----------|-------------|
| `RIDER_BOT_TOKEN` | [@BotFather](https://t.me/BotFather) token for the rider bot |
| `DRIVER_BOT_TOKEN` | BotFather token for the driver bot |
| `DATABASE_URL` | **or** `TURSO_DATABASE_URL` + `TURSO_AUTH_TOKEN` — Turso / libSQL connection string |

### Common optional

| Variable | Default / notes |
|----------|------------------|
| `WEBAPP_URL` | Base URL for Mini App (HTTPS in production), e.g. `https://your-app.vercel.app/webapp` |
| `RIDER_MAP_URL` | If empty, derived from `WEBAPP_URL` + `/rider-map.html` |
| `API_ADDR` | Listen address; `:8080`. If the platform sets **`PORT`**, it is used (e.g. Render/Railway). |
| `STARTING_FEE`, `PRICE_PER_KM` | Legacy/config fare when DB fare settings are absent |
| `MATCH_RADIUS_KM`, `EXPANDED_RADIUS_KM`, `RADIUS_EXPANSION_MINUTES` | Dispatch radii |
| `REQUEST_EXPIRES_SECONDS`, `DRIVER_SEEN_SECONDS` | Request TTL and driver visibility window |
| `ENABLE_DRIVER_ID_HEADER` | **Default on in code** (unset = `X-Driver-Id` allowed). Set **`false`**, **`0`**, **`no`**, or **`off`** to require Telegram initData only (stricter production) |
| `DRIVER_AUTH_DEBUG` | `true` / `1` to log boolean flags `driver_header_path_enabled` and `x_driver_id_header_present` per path (never logs header value or ids) |
| `ENABLE_DRIVER_HTTP_LIVE_LOCATION` | `true` / `1` so **`POST /driver/location`** refreshes live-location columns and drivers can be matched; also **records offers in the DB if Telegram `Send` fails** so **`GET /driver/available-requests`** works for web/native clients; default off |
| `ADMIN_BOT_TOKEN`, `ADMIN_ID` | Optional admin bot + Telegram user id for fare admin flows |
| `INFINITE_DRIVER_BALANCE` | If `true`, dispatch ignores balance and trip commission is skipped |
| `COMMISSION_PERCENT` | Platform commission on normalized fare when infinite balance is off |
| `DISPATCH_WAIT_SECONDS`, `DISPATCH_DRIVER_COOLDOWN_SECONDS` | Batch wait and per-driver cooldown |
| `PICKUP_START_MAX_METERS` | Default **100**. Driver must be within this distance (meters) of the rider pickup to **`POST /trip/start`** from **`WAITING`**, or to **`POST /trip/arrived`**. Uses **Telegram live location** on the server (`drivers.last_lat` / `last_lng`, `live_location_active`, `last_live_location_at` ≤ **90s**). |

Secrets must not be committed. This repo’s `.gitignore` ignores `.env*`.

---

## Run locally

### Docker

```bash
docker compose up --build
```

On **Windows** (PowerShell), use **`docker compose`** (with a space). Exposes the API (typically **8080**). Set Turso (or local libSQL) variables in **`.env`**.

### Migrations first

Apply schema before relying on the app:

```bash
go run ./cmd/migrate -up
```

---

## Database and migrations

- **Engine:** Turso / libSQL (SQLite-compatible).
- **Tool:** [goose](https://github.com/pressly/goose); files in **`db/migrations/`**.
- **Runner:** `go run ./cmd/migrate -up` (with **`DATABASE_URL`** / **`TURSO_*`** in **`.env`**). The repo **`Makefile`** exposes **`make migrate-up`** (it loads **`.env`** when present), but its **default `DATABASE_URL`** is a legacy **Postgres** example — **override it** for this project so migrations hit your Turso DB.

**Startup repair** (non-destructive helpers) runs in `cmd/app` for legal schema, **`driver_ledger`** column names, and missing indexes (see **`internal/db/ledgerrepair`**).

Notable migration themes:

| Area | Examples |
|------|----------|
| Drivers / verification | `025_driver_verification.sql`, application steps, legal fingerprints |
| Promo / ledger | `035_driver_promo_cash_ledger.sql`; `038` first-3-trip ledger uniqueness; `040` referral ledger uniqueness; **`042_driver_ledger_unique_driver_ref.sql`** — global **`UNIQUE (driver_id, reference_type, reference_id)`** on **`driver_ledger`**, with an **Up** step that backfills legacy **commission** rows (`reference_type = 'trip'`) so each line uses **`reference_id = '<trip_id>:' || entry_type`** before the index is created (re-run safe for already-normalized rows) |
| Referrals | `017_referral_fields.sql`, `019_driver_referral_reward_stages.sql`, **`039_driver_referrals.sql`** |
| Legal | `034_legal_documents_schema_rebuild.sql` and later legal admin versions |
| Trips | **`041_trips_arrived_status.sql`** — adds **`ARRIVED`** trip status and **`arrived_at`** (pickup-before-start flow) |

---

## HTTP API reference

Public prefixes include **`/admin/...`** (dashboard), **`/api/...`**, **`/v1/...`** — see **`internal/handlers`** and **`internal/server`**.

### Health and static

| Method | Path | Auth |
|--------|------|------|
| `GET` / `HEAD` | `/health`, `/` | No |
| `GET` | `/webapp/*` | Static files from `./webapp` |
| `GET` | `/ws?trip_id=...` | Telegram initData (driver or rider on trip); **`X-Driver-Id`** when header mode is on (default unless env disables it) |

### Trip and driver (Mini App / bots)

Driver routes use **`tryDriverID`** then **`RequireDriverAuth`** (Telegram initData and/or **`X-Driver-Id`**; header path on by default, set **`ENABLE_DRIVER_ID_HEADER=false`** to disable).

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/trip/:id` | Trip info (`status` may be **`WAITING`**, **`ARRIVED`**, **`STARTED`**, …) |
| `POST` | `/driver/location` | Driver location ping (map tracking). Dispatch freshness uses Telegram live unless **`ENABLE_DRIVER_HTTP_LIVE_LOCATION`** (see **`docs/AUTH.md`**) |
| `POST` | `/trip/arrived` | **`WAITING` → `ARRIVED`**: server checks pickup distance + fresh Telegram live location (same rules as starting from `WAITING`). Optional explicit “at pickup” step. Body: `{ "trip_id" }`. |
| `POST` | `/trip/start` | **`WAITING` or `ARRIVED` → `STARTED`**. From **`WAITING`**, server enforces near-pickup + live location (do not rely on Mini App alone). From **`ARRIVED`**, proximity is **not** re-checked. Distance/fare accumulation still only after **`STARTED`**. On failure: e.g. too early → **400** with Uzbek message *“Mijozga hali yetib bormagansiz…”* |
| `POST` | `/trip/finish` | Finish trip: **trip → `FINISHED`**, first-3 promo, referral check, and **commission** run in **one DB transaction**; if any of those steps fails, the transaction rolls back (trip stays non-**`FINISHED`**). **Telegram** notifications run **after** a successful commit. |
| `POST` | `/trip/cancel/driver` | Driver cancel |
| `POST` | `/trip/cancel/rider` | Rider cancel (`riderAuth`) |
| `GET` | `/driver/referral-link` | JSON `{ "referral_link": "..." }` |
| `GET` | `/driver/promo-program` | Signup + first-3-trip promo progress + `promo_balance` |
| `GET` | `/driver/referral-status` | If referred: inviter id, finished trip count, threshold 3, reward granted flag |
| `GET` | `/driver/available-requests` | Optional **`assigned_trip`** (`trip_id`, `status`); queue arrays **`available_requests`**, **`requests`**, **`pending_requests`**, **`queue`**, **`orders`**, **`jobs`** (same items; client may merge/dedupe by `request_id`) |
| `POST` | `/driver/accept-request` | Body **`request_id`** (accept offer) and/or **`trip_id`** (idempotent if already assigned). Uses same **`TryAssign`** as driver bot. **409** if request taken/expired |

### Legal (Mini App)

| Method | Path | Auth |
|--------|------|------|
| `GET` | `/legal/active` | `appUserAuth` (+ optional `X-Driver-Id`) |
| `POST` | `/legal/accept` | Same |

**Document sets (split privacy):**
- **Drivers** see and accept **`driver_terms`** (haydovchi oferta) + **`privacy_policy_driver`**
- **Riders** see and accept **`user_terms`** + **`privacy_policy_user`**

Dispatch and driver live-location gating require the **driver** pair at active versions (see **`internal/legal`**, **`SQLDriverDispatchLegalOK`**).

CORS (see **`server.go`**): allows `Authorization`, `X-Telegram-Init-Data`, `X-Driver-Id`. Full auth behavior: **`docs/AUTH.md`**.

---

## Deployment

You deploy **(A)** static Mini App and **(B)** one **backend** process talking to **Turso**.

### A. Mini App (e.g. Vercel)

- Root **`webapp/`**, static output.
- Set **`API_BASE`** in **`webapp/map.js`** (and rider map if needed) to your **HTTPS** API origin (no trailing slash).

### B. Backend (Railway / Render / Fly / VPS)

1. Set **`RIDER_BOT_TOKEN`**, **`DRIVER_BOT_TOKEN`**, **Turso** URL + token (or **`DATABASE_URL`**).
2. Set **`WEBAPP_URL`** to the **public** Mini App base (so “Open map” links work).
3. Run migrations once against the **same** DB: `go run ./cmd/migrate -up`.
4. **Telegram:** only **one** `getUpdates` consumer per bot token. On **Render**, set **instance count = 1** (see **`render.yaml`**). Duplicate instances cause `Conflict: terminated by other getUpdates request`.

### Connect everything

| Component | Value |
|-----------|--------|
| Mini App URL | e.g. `https://your-app.vercel.app/webapp` |
| Backend API | e.g. `https://your-service.onrender.com` |
| Backend **`WEBAPP_URL`** | Mini App base URL (HTTPS) |
| **`webapp/map.js`** **`API_BASE`** | Backend API origin |

### Keepalive / ping (Render sleep + “Output too large”)

**Wake the backend, not the static site.** Free/low-usage hosts often **spin down** the API. Uptime/ping must hit the **same origin as your API** (Mini App / Vercel pages return **large HTML** and are the wrong target).

- **Ping URL:** `https://<your-backend-host>/health` — response is plain text **`OK`** (2 bytes).
- **External monitors (UptimeRobot, etc.):** HTTP GET to that URL only; do not use checks that store or match huge response bodies.
- **Shell / scheduled jobs:** discard the body so job output stays tiny (avoids platform limits like **“Output too large”**):

```bash
curl -fsS -o /dev/null "https://your-backend-host.onrender.com/health"
```

`-f` treats non-2xx as failure; `-sS` keeps stderr quiet except errors; `-o /dev/null` prints **no** body to stdout.

If the service was asleep, the **first** request may take longer (cold start); that is normal.

**Optional:** this repo’s **`render.yaml`** includes a small **cron** job that pings `/health` the same way (adjust the hostname if you rename the web service).

### Deployment troubleshooting

| Symptom | Likely cause | Fix |
|---------|----------------|-----|
| `no such table: ...` | Empty DB / migrations not applied | Run `go run ./cmd/migrate -up` with production DB URL |
| `UNIQUE constraint failed` on **`042_driver_ledger_unique_driver_ref`** | Old DBs had several **commission** ledger rows per trip with the same **`reference_type` / `reference_id`** | Use the **current** `042` from this repo: it **normalizes** those rows **before** `CREATE UNIQUE INDEX`. Redeploy after pull; do not drop the backfill `UPDATE` steps from `042`. |
| `getUpdates` conflict | Two replicas or local + cloud | Single instance; stop duplicate processes |
| Render build cache errors | Stale cache | Clear build cache, redeploy |
| Service “sleeps” / cold start | Idle spin-down (e.g. Render free) | Ping **`/health`** on the **backend** URL every few minutes (see **Keepalive / ping** above); not the Vercel Mini App |
| Cron / job **“Output too large”** | Command prints full HTTP body (HTML) or `curl -v` | Use `curl -fsS -o /dev/null ...` (or equivalent); point at **`/health`** |

---

## Architecture

| Layer | Path |
|--------|------|
| Handlers | `internal/handlers/` |
| Services | `internal/services/` |
| Repositories | `internal/repositories/` |
| Models | `internal/models/` |
| WebSocket | `internal/ws/` |
| Bots | `internal/bot/` (rider, driver, admin) |
| Auth | `internal/auth/` |
| Config | `internal/config/` |

**Trip flow:** PENDING request → assigned driver → trip **`WAITING`** → (optional **`POST /trip/arrived`** → **`ARRIVED`**) → **`POST /trip/start`** → **`STARTED`** → **`FINISHED`** (or cancelled). The server rejects **`STARTED`** from **`WAITING`** unless the driver is near pickup with fresh Telegram live location (see **`PICKUP_START_MAX_METERS`**), or the trip is already **`ARRIVED`**. Live route distance and fare math apply only after **`STARTED`**. WebSocket events include location updates, start, finish, cancel.

---

## Fare and commission

- **Fare:** Distance-based; may use **`FareService`** DB settings when present, otherwise config **`STARTING_FEE`** / **`PRICE_PER_KM`** (and rounding rules in code).
- **Commission:** Applied inside the same **`FinishTrip`** DB transaction as the status flip and promo/referral steps (so a failed commission write rolls the trip back from **`FINISHED`**). Ledger rows use **`reference_type = 'trip'`** and **`reference_id = '<trip_id>:' || entry_type`** (e.g. **`…:COMMISSION_ACCRUED`**) so each line is unique under **`042`**. Accrual and offsets **`PROMO_APPLIED_TO_COMMISSION`** / **`CASH_APPLIED_TO_COMMISSION`** are **not** bank settlement. Legacy **`payments`** rows may still be written for admin views; **ledger is authoritative** for bucket behavior.

---

## Driver program: promo and referral (YettiQanot)

All amounts below are **promo platform credit** unless stated otherwise: **not real money**, **not withdrawable**, **not** paid via `cash_balance` or payouts.

### Onboarding promo

| Rule | Amount | Idempotency |
|------|--------|-------------|
| Once on **approval** | **+20 000** promo | `signup_bonus_paid` + ledger `signup_promo` |
| **1st / 2nd / 3rd** finished trip | **+10 000** each | Ledger `first_3_trip_bonus` + **`UNIQUE (driver_id, reference_type, reference_id)`** + **`INSERT OR IGNORE`** (`reference_id` = trip id) |

**Code:** `internal/accounting/driver_promo.go`, **`GET /driver/promo-program`**.

### Referral reward (inviter)

| Rule | Amount | Idempotency |
|------|--------|-------------|
| Referred driver completes **3** trips **FINISHED**, **`verification_status = approved`** | Inviter **+20 000** promo | `users.referral_stage2_reward_paid` on **referred** user + ledger `referral_reward` + **`INSERT OR IGNORE`** on the same **unique** ledger key |

- **Relation:** **`driver_referrals`** (`inviter_user_id`, **`referred_user_id` UNIQUE**), backfilled from `users.referred_by` / `referral_code`.
- **Trigger:** **`TripService.FinishTrip`** — first-3 promo, referral grant, and commission run in the **same transaction** as setting the trip to **`FINISHED`**. If any step errors, the whole transaction rolls back.
- **Telegram** to inviter (and other finish notifications): **only after** a successful **commit**.

**Code:** `internal/accounting/referral_reward.go`, **`GET /driver/referral-status`**.

### Removed or disabled (do not re-enable without product review)

- ~100k signup, 80k five-trip milestone, hourly **online bonus** worker (no-op stub remains for wiring).

### Ledger quick reference

| `reference_type` (examples) | Meaning |
|-----------------------------|--------|
| `signup_promo` | Approval onboarding grant |
| `first_3_trip_bonus` | Per-trip bonus, `reference_id` = trip id |
| `referral_reward` | Inviter reward, `reference_id` = referred **user** id (string) |
| `trip` | Commission accrual / offsets; `reference_id` is **`<trip_id>:<ENTRY_TYPE>`** (e.g. `abc123:COMMISSION_ACCRUED`) |

---

## Manual test checklist

1. **Start:** `docker compose up --build` (or local binary) with valid **Turso** env and **migrations applied**.
2. **Rider:** Create request; confirm pricing and dispatch behavior.
3. **Driver:** Tap **«Jonli lokatsiyani ulashish»** (plain reply label: bot always sends the **full** illustrated guide; it does not open the map or change live share). Turn on **live** location via 📎 for real online/dispatch; accept request; open Mini App; near pickup, **`/trip/start`** (or **`/trip/arrived`** then **`/trip/start`**); **finish** trip.
4. **Finish:** Rider and driver notifications; **promo** and **referral** grants visible in DB (`drivers.promo_balance`, `driver_ledger`).
5. **Admin / legal:** If used, verify verification and legal acceptance flows.
6. **Shutdown:** SIGINT/SIGTERM; process exits cleanly.
7. **Optional native driver client:** **`X-Driver-Id`** is enabled by default; use **`ENABLE_DRIVER_HTTP_LIVE_LOCATION`** when the app streams GPS without Telegram live. To harden Telegram-only deploys, set **`ENABLE_DRIVER_ID_HEADER=false`**.

---

## Developer notes

### Legal schema

If logs mention missing **`document_type`** or broken legal tables, run migrations through **`034_legal_documents_schema_rebuild.sql`** (and later). That rebuild **clears** legal acceptance rows — users re-accept active documents.

### Admin and legal HTTP API

Admin routes (drivers, riders, payments, verification) are registered from **`handlers.NewAdminHandlers`**. Legal admin endpoints are under **`/admin/legal/...`** and mirrored paths (see **`RegisterAdminLegalRoutes`**). **Source of truth** for compliance is **`legal_acceptances`** vs **active** **`legal_documents`**.

### Driver bot UX (live location)

- **Online** for matching = **Telegram live location** freshness (+ balance + legal + approval).
- Reply keyboard **«Jonli lokatsiyani ulashish»** is a **plain text** button (`internal/driverloc.ReplyKeyboardButtonShareLiveLocation`): every press triggers the **full** photo + caption guide, including if live location is already active; the bot does **not** start/stop or “restart” live location. Actual live share remains 📎 → Location → *Share Live Location* (see **`internal/driverloc/texts.go`** and embedded **`live_location_steps.png`**).
- Instruction **image** is embedded at build time: **`internal/bot/driver/live_location_steps.png`** (`//go:embed` in **`internal/bot/driver/bot.go`**). Replace that file to update the screenshot guide; keep captions within Telegram’s **~1024** character caption limit.
- Pinned **status** message is edited when possible; **`/status`** refreshes it.
- **`driver_ledger`** and **`GET /admin/drivers/:id/ledger`** expose promo vs cash audit.

### Driver vs rider legal acceptance

- **Drivers:** **`driver_terms`** + **`privacy_policy_driver`** at active versions (Telegram oferta prompt, Mini App **`/legal/*`**, dispatch SQL). **`user_terms`** are **not** required for drivers.
- **Riders:** **`user_terms`** + **`privacy_policy_user`**.

### Migration notes (SQLite / goose)

- Some migrations (e.g. **`048_split_privacy_policy_user_driver.sql`**) rebuild tables to extend SQLite `CHECK` constraints. Those are marked **`-- +goose NO TRANSACTION`** because SQLite does **not** allow starting a transaction inside an existing one (nested `BEGIN`), and goose normally wraps migrations in a transaction by default.

### Telegram Bot API (message length)

Telegram caps **`sendMessage`** / **`editMessageText`** text at about **4096 characters**. Very long user-visible strings (e.g. stacked notifications, legal text, or AI-generated replies) can fail with errors such as **“output too large.”** Prefer concise copy in trip/promo/referral flows; split or truncate at the application layer if you add verbose content. **Photo captions** use a **shorter** limit (~**1024** characters) — keep captions short or send overflow as separate messages.

### Schema drift helpers

- **`internal/db/legalrepair`**, **`legalfingerrepair`**, **`ledgerrepair`** — run at startup to align common drift (never substitute for running **goose** migrations on new environments).

### Finish trip atomicity

- **`internal/services/trip_service.go`** (`FinishTrip`) and **`internal/accounting/trip_finish_grants.go`** / **`wallet.go`** — one **`sql.Tx`** for **finish → effects**; Telegram side effects stay outside the transaction.
- Tests: **`internal/accounting/trip_finish_atomic_test.go`** (simulated failures for promo / referral / commission rollback; success path commits all effects).

### Further reading

- **`docs/AUTH.md`** — authentication, **`X-Driver-Id`**, protected routes.
- **`PLAN.md`** / **`render.yaml`** — internal planning and Render blueprint hints.

### GitHub Actions

CI workflows are optional; this repo may or may not define **`.github/workflows/*.yml`**. Run **`go test ./...`** locally before pushing.

---

## License / project

Internal YettiQanot / taxi MVP codebase; adjust licensing as your organization requires.
