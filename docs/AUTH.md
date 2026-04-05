# Authentication and Authorization

## 1. File-by-file plan

| File | Purpose |
|------|---------|
| **internal/auth/verify.go** | `VerifyMiniAppInitData(botToken, initData)` — HMAC-SHA256 verification of Telegram initData; returns `telegram_user_id`. |
| **internal/auth/context.go** | `User` struct (user_id, telegram_user_id, role); `WithUser` / `UserFromContext` for request context. |
| **internal/auth/resolve.go** | `ResolveUserFromTelegramID(ctx, db, telegramID)` → (user_id, role); `AuthorizeTripAccess(ctx, db, userID, tripID, role)` → true if user is driver or rider of trip. |
| **internal/auth/middleware.go** | `RequireMiniAppAuth(db, botToken)`; `RequireDriverAuth(...)` — if user already driver in context (from `TryDriverIDHeader`), continues; else **Telegram initData only** (X-Driver-Id is handled only in `TryDriverIDHeader`). `RequireRiderAuth` uses rider bot token only. |
| **internal/auth/driver_x_header.go** | `ResolveDriverUserForXDriverID` — single resolver for HTTP + WS: tries internal **`users.id`** then **`users.telegram_id`**, requires **`drivers.verification_status = approved`**. `TryDriverIDHeader(db, opts)` runs first on driver routes; optional `DRIVER_AUTH_DEBUG` logs only booleans (never the header value). |
| **internal/handlers/trip.go** | Trip handlers take `db`; get `User` from context; use `auth.AuthorizeTripAccess`; call services with `u.UserID` (no driver_id/rider_id from body). Request bodies: only `trip_id`. |
| **internal/handlers/driver_location.go** | Get driver from context; body only `lat`, `lng`, `accuracy` (no `driver_id`). |
| **internal/ws/handler.go** | `ServeWsWithAuth(hub, db, driverToken, riderToken, enableDriverIDHeader, w, r)` — before upgrade: initData (driver or rider token) **or**, when `enableDriverIDHeader` and initData absent, `X-Driver-Id` for assigned driver; then `AuthorizeTripAccess`; 401/403 on failure; upgrade. |
| **internal/server/server.go** | Apply `tryDriverID` then `driverAuth` on driver routes; `riderAuth` on rider cancel; GET /ws → `ServeWsWithAuth`. CORS allows `Authorization`, `X-Telegram-Init-Data`, `X-Driver-Id`. |

## 2. Helper functions

- **ResolveUserFromTelegramID** — `internal/auth/resolve.go`
- **VerifyMiniAppInitData** — `internal/auth/verify.go`
- **AuthorizeTripAccess** — `internal/auth/resolve.go`

## 3. Protected endpoints

- `POST /trip/start` — driver auth; body `{ "trip_id" }`; driver may only start their assigned trip.
- `POST /trip/finish` — driver auth; body `{ "trip_id" }`; driver may only finish their assigned trip.
- `POST /trip/cancel/driver` — driver auth; body `{ "trip_id" }`.
- `POST /trip/cancel/rider` — rider auth; body `{ "trip_id" }`.
- `POST /driver/location` — driver auth; body `{ "lat", "lng", "accuracy?" }`.
- `GET /driver/promo-program` — driver auth; JSON promo program status (`promo_balance`, signup flag, first-three trip bonus progress).
- `GET /driver/referral-status` — driver auth; JSON for referred drivers: inviter id, finished trip count, threshold 3, whether inviter reward was already granted.
- `GET /driver/available-requests` — driver auth; JSON with optional `assigned_trip` and queue arrays (see README).
- `POST /driver/accept-request` — driver auth; body `{ "request_id" }` and/or `{ "trip_id" }` (idempotent “already assigned” when only `trip_id` matches); uses **`AssignmentService.TryAssign`** (same as driver bot).
- `GET /ws?trip_id=...` — **initData** (header or query) **or** `X-Driver-Id` when **`ENABLE_DRIVER_ID_HEADER`** (driver on trip only); only rider or assigned driver may connect.

### Standalone / Flutter driver app (additive)

- **Header (exact name):** `X-Driver-Id` — digits only: either internal **`users.id`** (same as `drivers.user_id`, admin dashboard user id) **or** the driver’s **Telegram numeric user id** (`users.telegram_id`). Lookup tries internal id first, then Telegram id. **`drivers.verification_status` must be `approved`** or the API returns **403** `{"error":"driver not approved"}` (not the initData error).
- **Env:** **`X-Driver-Id` is on by default** (no env required). Set **`ENABLE_DRIVER_ID_HEADER=false`** (or **`0`**, **`no`**, **`off`**) on the server to require Telegram initData only. Optional **`DRIVER_AUTH_DEBUG=true`** logs `driver_header_path_enabled` and `x_driver_id_header_present` per request path only.
- **curl (approved driver, default config):** `curl -sS -H "X-Driver-Id: YOUR_ID" "$BASE/driver/promo-program"` → **200** JSON. **Header mode disabled** (`ENABLE_DRIVER_ID_HEADER=false`) with only header → **401** `missing or invalid Telegram init data`. **Unknown id** → **401** `unknown driver id...`. **Wrong format** → **401** `invalid X-Driver-Id...`.
- **Telegram-only hardening:** set **`ENABLE_DRIVER_ID_HEADER=false`** so `X-Driver-Id` is ignored; Mini App / WebView must send **`X-Telegram-Init-Data`**.
- **Security:** with header mode on (default), anyone who can guess or leak ids could impersonate a driver — use HTTPS, app attestation, network rules, monitoring, and edge rate limits where needed.
- **`Authorization: Bearer ...`:** not validated by this backend today; allowed in CORS for forward compatibility if you add a gateway or future server support.
- **Dispatch eligibility:** by default, grid dispatch expects Telegram live-location freshness. For HTTP GPS from a native app, set **`ENABLE_DRIVER_HTTP_LIVE_LOCATION=true`** so **`POST /driver/location`** updates **`last_live_location_at`** / **`live_location_active`**, marks eligible drivers online, and (if Telegram `Send` fails for an offer) a **`request_notifications`** row is still written so **`GET /driver/available-requests`** can show the job. Default **off** preserves Telegram-first behavior.

## 4. Example request flow

**Driver starts trip – “Safarni boshlash” (Mini App)**

The backend sets the driver in the auth context so trip start/location/finish/cancel work. Two options:

**Option 1: Telegram initData (recommended)**

1. Mini App has `Telegram.WebApp.initData` (from Telegram when the app is opened).
2. Client sends `POST /trip/start` with header **`X-Telegram-Init-Data: <initData>`** and body `{ "trip_id": "uuid" }`.
3. **Middleware** reads initData, validates it with the **driver bot token** (HMAC-SHA256), gets Telegram `user.id`, maps it to internal `user_id` via `users.telegram_id`, and sets context `User{ user_id, telegram_user_id, role: "driver" }`.
4. Handler gets `User` from context; `AuthorizeTripAccess`; then `tripSvc.StartTrip(ctx, trip_id, user_id)`.
5. Response 200 with trip result. If the Mini App does not send `X-Telegram-Init-Data`, requests return 401 until initData is sent.

**Option 2: X-Driver-Id (optional, only if you trust the client)**

1. Header mode is **on by default**; only set **`ENABLE_DRIVER_ID_HEADER=false`** if you want to disable it.
2. Client sends header **`X-Driver-Id`** (digits only): internal **`users.id`** or Telegram **`users.telegram_id`**, resolved in that order; driver must be **`verification_status = approved`**.
3. **`TryDriverIDHeader`** runs before **`RequireDriverAuth`** on driver routes; if the header is present and invalid/unknown, the request fails with a specific **401/403** JSON error (not the generic initData string).
4. Use this only from a trusted origin (HTTPS native app or Mini App). Do not enable in untrusted environments.

**Rider cancels trip**

1. Client (rider) sends `POST /trip/cancel/rider` with `X-Telegram-Init-Data` and body `{ "trip_id": "uuid" }`.
2. Middleware verifies with **rider** bot token, resolves rider user, sets context.
3. Handler checks role is rider and `AuthorizeTripAccess` for (user_id, trip_id, "rider"); then `CancelByRider(ctx, trip_id, user_id)`.

**WebSocket subscribe**

1. Client connects to `GET /ws?trip_id=uuid` with **`X-Telegram-Init-Data`** (or `init_data=...` in query), **or** with **`X-Driver-Id`** when header mode is enabled (default; driver must be assigned to that trip).
2. **Before upgrade**: verify initData with driver then rider token, **or** resolve driver from `X-Driver-Id`; then `AuthorizeTripAccess(user_id, trip_id, role)`.
3. If allowed, upgrade to WebSocket and register client for that trip_id; otherwise 401/403 and connection not upgraded.

**Driver location**

1. `POST /driver/location` with header `X-Telegram-Init-Data` and body `{ "lat": 41.2, "lng": 69.3 }`.
2. Middleware sets driver in context; handler uses `u.UserID` for DB update and AddPoint.
