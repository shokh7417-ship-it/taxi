# Taxi MVP

## Setup

1. Copy the example env file and add your bot tokens:

   ```bash
   cp .env.example .env
   ```

2. Edit `.env` and set:
   - `RIDER_BOT_TOKEN` — Telegram bot token for the rider bot
   - `DRIVER_BOT_TOKEN` — Telegram bot token for the driver bot
   - **Turso database:** `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` (from [Turso](https://turso.tech)), or a single `DATABASE_URL=libsql://your-db.turso.io?authToken=...`
   - `WEBAPP_URL` — Base URL where the webapp is served (e.g. `https://your-domain.com/webapp`). The backend serves static files at `r.Static("/webapp", "./webapp")`. Bot buttons open the **actual HTML files**: Driver → `WEBAPP_URL/index.html?trip_id=...&driver_id=...`, Rider → `WEBAPP_URL/rider-map.html?trip_id=...`. Must be HTTPS for Telegram Web App. Do not use the API base URL here—use the URL that serves the Mini App HTML.
   - `API_ADDR` — HTTP API address (default `:8080`) for trip and driver location endpoints.

## Run with Docker

```bash
docker compose up --build
```

This starts the **app** (rider + driver bots + API). The app connects to **Turso** (libSQL); set `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` in `.env`. Port 8080 is exposed for the API.

## Run migrations (Turso)

Migrations use [goose](https://github.com/pressly/goose) with **SQLite dialect** and live in `db/migrations/`. Point at your Turso database:

**Using the Go migration runner (recommended):**

```bash
# From project root; uses DATABASE_URL or TURSO_DATABASE_URL + TURSO_AUTH_TOKEN from .env
go run ./cmd/migrate -up
go run ./cmd/migrate -down   # rollback last migration
```

**Using Make:**

```bash
make migrate-up
make migrate-down
```

Create a database at [turso.tech](https://turso.tech), then set `TURSO_DATABASE_URL` (e.g. `libsql://your-db-your-org.turso.io`) and `TURSO_AUTH_TOKEN` in `.env` before running migrations.

## Deployment

You deploy two parts: the **Mini App** (static frontend) and the **backend** (Go API + Telegram bots + PostgreSQL).

### 1. Deploy Mini App (Vercel)

The Mini App is the map UI drivers open after accepting a ride.

1. **Push your repo** to GitHub (if not already).
2. In [Vercel](https://vercel.com): **Add New Project** → Import your repo.
3. **Configure:**
   - **Root Directory:** `webapp`
   - **Framework Preset:** Other
   - **Build Command:** leave empty
   - **Output Directory:** `.`
4. **Deploy.** You’ll get a URL like `https://your-app.vercel.app`.
5. **Point the Mini App at your backend:**  
   In `webapp/map.js`, set `API_BASE` to your Go backend URL (no trailing slash), e.g.:
   ```javascript
   var API_BASE = 'https://your-backend.railway.app';
   ```
   Commit and push; Vercel will redeploy.

### 2. Deploy Backend + Telegram Bots (Railway / Render / Fly.io / VPS)

The backend runs the Go app (HTTP API on port 8080), rider bot, driver bot, and uses **Turso** (libSQL) as the database.

**Option A: Railway**

1. Create a project at [railway.app](https://railway.app).
2. **Turso:** Create a database at [turso.tech](https://turso.tech) and copy `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` (or set a single `DATABASE_URL=libsql://...?authToken=...`).
3. **Go app:** Add a service → **GitHub Repo** (this repo) → **Dockerfile**.  
   Railway will build the Dockerfile. Set **Root Directory** to repo root so it sees `Dockerfile` and `docker-compose` if needed.
4. **Variables** for the app service:
   - `RIDER_BOT_TOKEN` — from [@BotFather](https://t.me/BotFather)
   - `DRIVER_BOT_TOKEN` — from BotFather
   - `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` (or `DATABASE_URL` with full libsql URL)
   - `WEBAPP_URL` — your Mini App URL, e.g. `https://your-app.vercel.app`
   - Other vars from `.env.example` as needed (e.g. `PRICE_PER_KM`, `MATCH_RADIUS_KM`).
5. **Port:** The app listens on `API_ADDR` (default `:8080`). If the platform sets `PORT` (e.g. Railway, Render), the app uses it automatically.
6. **Migrations:** Run once (e.g. from your machine with `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` in `.env`):
   ```bash
   go run ./cmd/migrate -up
   ```
7. Copy the **public URL** of the app (e.g. `https://your-project.railway.app`) and use it as `API_BASE` in `webapp/map.js` (step 1.5 above). Ensure the backend allows CORS from your Vercel domain (your Gin app already uses a permissive CORS for development; restrict in production if desired).

**Option B: Render**

1. **Turso:** Create a database at [turso.tech](https://turso.tech); note `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN`.
2. **Web Service:** New **Web Service** → connect repo → use **Docker**. The Dockerfile runs **migrations automatically on startup** (so you no longer get `no such table: ride_requests` on first deploy).
3. **Environment:** Add the same variables as in Railway; set `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN`. Set `WEBAPP_URL` to your Vercel Mini App URL.
4. **Use exactly 1 instance.** Telegram allows only one `getUpdates` connection per bot. In Render: **Settings** → **Scaling** → set **Instance count** to **1**. If you see "Conflict: terminated by other getUpdates request", you have more than one instance or the same bots running elsewhere — set instance count to **1** and restart. You can use the provided `render.yaml` (Blueprint) which sets `numInstances: 1` by default.
5. Render will assign a URL like `https://your-service.onrender.com`. Use that as `API_BASE` in `map.js`.

**Option C: VPS (e.g. Ubuntu + Docker)**

1. On the server, clone the repo and copy `.env.example` to `.env`. Fill in bot tokens, `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` (or `DATABASE_URL` with full Turso URL), and `WEBAPP_URL=https://your-app.vercel.app`.
2. Run:
   ```bash
   docker compose up -d --build
   ```
3. Run migrations (from server or your machine with Turso env vars set):
   ```bash
   go run ./cmd/migrate -up
   ```
4. Expose port **8080** (e.g. nginx reverse proxy or firewall). The public URL (e.g. `https://api.yourdomain.com`) is your `API_BASE` in `map.js`.

### 3. Connect Everything

| What | Value |
|------|--------|
| **Mini App (Vercel)** | `https://your-app.vercel.app` |
| **Backend API** | `https://your-backend.railway.app` (or Render/VPS URL) |
| **In backend `.env`** | `WEBAPP_URL=https://your-app.vercel.app` (so the “Open Trip Map” button opens this URL with `?trip_id=...&driver_id=...`) |
| **In `webapp/map.js`** | `API_BASE = 'https://your-backend.railway.app'` (so the Mini App calls your API) |

- Backend must be **HTTPS** in production so Telegram and browsers accept it.
- In **BotFather**, you can set the **Menu Button** or **Web App** URL for the driver bot to your Mini App URL so users can open the map from the bot menu.

### 4. Checklist

- [ ] Mini App deployed on Vercel; `map.js` has correct `API_BASE`.
- [ ] Backend deployed (Railway/Render/VPS); Turso env vars set, migrations run.
- [ ] `WEBAPP_URL` on backend = Vercel Mini App URL.
- [ ] Backend URL is HTTPS and reachable; CORS allows your Vercel origin.
- [ ] Rider and driver bots work in Telegram; driver sees “Open Trip Map” after accept and the map loads.

### 5. Troubleshooting (Render / deployment)

| Error | Cause | Fix |
|-------|--------|-----|
| `no such table: ride_requests` | Turso DB has no schema | If using the repo Dockerfile, migrations run on startup. If you use a custom build, run migrations once: `go run ./cmd/migrate -up` with the same `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN` as Render. |
| `Conflict: terminated by other getUpdates request` | Two+ instances or processes use same bot | Render: **Settings** → **Scaling** → **Instance count** = **1**. Stop any other app (e.g. local) using the same bot tokens. Restart the service. |
| `tar: Unexpected EOF in archive` / `cache download failed` | Render build cache corrupted or too large | Render: **Settings** → **Build & Deploy** → **Clear build cache**, then trigger a new deploy. Build will run without cache and recreate it. |

## Architecture (MVP)

Single Go service; no microservices.

| Layer | Path | Role |
|-------|------|------|
| **Handlers** | `internal/handlers/` | HTTP only: bind JSON, call services, return status/body |
| **Services** | `internal/services/` | Business logic: dispatch, assign, trip lifecycle, expiry, notify |
| **Repositories** | `internal/repositories/` | Database queries only (e.g. `TripRepo.CancelByDriver`) |
| **Models** | `internal/models/` | Domain structs (Trip, RideRequest) |
| **WebSocket** | `internal/ws/` | Hub, `/ws?trip_id=xxx`, broadcast events (driver_location_update, trip_started, trip_finished, trip_cancelled) |
| **Bot** | `internal/bot/` | Rider and driver Telegram bots |
| **Config** | `internal/config/` | Env-loaded config |

- **Dispatch:** Priority (closest driver first), 8s wait per driver; `request_notifications` tracks SENT/ACCEPTED/REJECTED/TIMEOUT.
- **Accept:** Atomic `UPDATE ride_requests SET status='ASSIGNED' WHERE id=? AND status='PENDING'`; check rows affected to avoid races.
- **Expiry:** Background worker every 5s; PENDING requests past `expires_at` → EXPIRED; rider notified "Haydovchi topilmadi."
- **Cancel:** `POST /trip/cancel/driver` and `POST /trip/cancel/rider`; trip status CANCELLED_BY_DRIVER / CANCELLED_BY_RIDER; driver set available; other party notified.
- **Rate limits:** Rider — 1 active (PENDING) request; driver — max 1 notification per 5 seconds.

## Fare calculation

Trip fare is computed from distance and `PRICE_PER_KM` (from `.env`):

- **Formula:** `fare_amount = ceil(distance_m / 1000) * PRICE_PER_KM`
- Distance is rounded up to full kilometers (e.g. 500 m → 1 km, 1001 m → 2 km).
- Implemented in `internal/utils/money.go` as `FareFromMeters(distanceM, pricePerKm)`.

## Test scenario steps

1. **Start the app**  
   Run `docker compose up --build` (or run the app locally with Postgres).

2. **Rider flow**  
   - Open the rider bot in Telegram.  
   - Send a ride request (e.g. set pickup and destination).  
   - Confirm the request is created and see price (based on `PRICE_PER_KM` and distance).

3. **Driver flow**  
   - Open the driver bot in Telegram.  
   - Ensure the driver is sharing **Telegram live location** (and is in the matching pool).  
   - Verify that new rider requests appear within `MATCH_RADIUS_KM` and that requests expire after `REQUEST_EXPIRES_SECONDS`.

4. **Matching**  
   - With a rider request active, have a driver in range.  
   - Check that the driver sees the request and can accept it.  
   - Confirm the rider is notified when a driver accepts.

5. **Expiry and visibility**  
   - Create a rider request and do not accept it; after `REQUEST_EXPIRES_SECONDS` it should expire.  
   - Confirm drivers are considered “seen” for matching within `DRIVER_SEEN_SECONDS` of their last activity.

6. **Driver Mini App (Trip Map)**  
   - After a driver accepts a ride in the driver bot, they get an **"Open Trip Map"** button that opens the Mini App.  
   - The Mini App (Leaflet + OpenStreetMap) shows: driver location, pickup marker, route driver→pickup, **START TRIP** and **FINISH TRIP** buttons.  
   - Driver location is sent to the backend every few seconds via `POST /driver/location`.  
   - **After FINISH TRIP:** When the driver presses "SAFARNI TUGATISH", the Mini App must call `POST /trip/finish` and may send `POST /driver/location` for map/trip tracking. **Matching and “online” for new orders use Telegram live location only** (`POST /driver/location` does not put the driver in the dispatch pool).  
   - Set `WEBAPP_URL` in `.env` to the public URL where `/webapp` is served (same server as the API).

7. **Shutdown**  
   - Send SIGINT/SIGTERM to the app (e.g. Ctrl+C).  
   - Verify both bots stop and the process exits without errors (graceful shutdown).

## Developer notes

### Legal DB schema (`document_type`)

If logs show `no such column: document_type` on `legal_documents`, the database has an incompatible older `legal_*` layout (or a partial deploy). Run migrations through **`034_legal_documents_schema_rebuild.sql`**, which drops and recreates `legal_documents`, `legal_acceptances`, and `legal_pending_resume` with the schema the app expects. **Legal acceptance rows are cleared**; users/drivers accept again after migrate.

### Driver bot: live location is the only “online” UX (YettiQanot)

- **Visible state:** A driver is treated as **online** only while **Telegram live location** is active (fresh `last_live_location_at`, same window as dispatch). There is **no** separate reply-keyboard “go online / go offline” flow; `drivers.is_active` for matching is still updated from the live-location handler but eligibility is **live location + balance + legal**.
- **Legal re-accept:** If legal changes interrupt an active live-location session, `legal_pending_resume.kind = driver_relive` is used. After acceptance we **clear** live/online fields and send a short message asking the driver to **re-share live location** (no generic “continue” and no auto-online). Telegram cannot resume a live session for the user.
- **Messaging:** The pinned **status card** is **edited in place** when possible; `/status` refreshes that pin instead of sending a duplicate full status. Live-location **step-by-step instructions** are sent only on first onboarding (when appropriate), when the driver taps **“Jonli lokatsiyani ulashish”**, or in the legal re-share case above—not after every trip.
- **Balances on the status card:** `drivers.balance` is the **sum** of `promo_balance` + `cash_balance`. **Promo** is internal promotional platform credit only (not real money, not withdrawable, not convertible to cash in app logic). **Cash** is the internal wallet for real top-ups when you add them later. The panel may still label totals as platform-oriented copy; admin API and `GET /admin/drivers/:id/ledger` expose the split and **append-only `driver_ledger`** (promo granted, commission accrued, promo applied to commission, etc.).

### Driver accounting model (YettiQanot — current stage)

- The platform is an **aggregator**; **live Click/bank settlement for customer fares is not implemented** in this version.
- **Startup approval bonus (~100k)** and milestones (e.g. five-trip bonus, referrer stage 2, online-time accrual) are **promotional platform credit** (`promo_balance`), recorded in **`driver_ledger`** as `PROMO_GRANTED` where applicable.
- **Commission** on a finished trip is **`COMMISSION_ACCRUED`** plus **`PROMO_APPLIED_TO_COMMISSION`** / **`CASH_APPLIED_TO_COMMISSION`** as internal offsets—not bank payouts or “deducted from customer payment.”
- **`promo_balance` must not** be withdrawn, transferred to `cash_balance`, or paid out; guards live in `internal/accounting` and admin **add-balance** tops up **cash** only (see `AdminService.AddDriverBalance` / `GrantCashTopUp`).
- Migration **`035_driver_promo_cash_ledger.sql`** adds `promo_balance`, `cash_balance`, and `driver_ledger`. After migrate, `balance` stays equal to `promo_balance + cash_balance`.
- **`payments`** rows for commission/deposit remain for legacy admin views; **`driver_ledger` is authoritative** for audit of promo vs cash behavior.
